package detectharness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/tailscale/hujson"
	"gopkg.in/yaml.v3"
)

type ownership string

const (
	ownershipAbsent  ownership = "absent"
	ownershipOwned   ownership = "owned"
	ownershipForeign ownership = "foreign"
)

func standardEntry(server StdioServer) map[string]any {
	entry := map[string]any{
		"command": server.Command,
		"args":    append([]string(nil), server.Args...),
	}
	if len(server.Env) > 0 {
		entry["env"] = cloneEnv(server.Env)
	}
	return entry
}

func serverEntry(kind entryKind, server StdioServer) map[string]any {
	switch kind {
	case entryOpenCode:
		command := make([]string, 0, len(server.Args)+1)
		command = append(command, server.Command)
		command = append(command, server.Args...)
		entry := map[string]any{"type": "local", "command": command}
		if len(server.Env) > 0 {
			entry["environment"] = cloneEnv(server.Env)
		}
		return entry
	case entryVSCode:
		entry := standardEntry(server)
		entry["type"] = "stdio"
		return entry
	default:
		return standardEntry(server)
	}
}

func cloneEnv(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func semanticEqual(left, right any) bool {
	return reflect.DeepEqual(normalizeValue(left), normalizeValue(right))
}

func normalizeValue(value any) any {
	if value == nil {
		return nil
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Interface, reflect.Pointer:
		if reflected.IsNil() {
			return nil
		}
		return normalizeValue(reflected.Elem().Interface())
	case reflect.Map:
		result := make(map[string]any, reflected.Len())
		iterator := reflected.MapRange()
		for iterator.Next() {
			key := fmt.Sprint(iterator.Key().Interface())
			result[key] = normalizeValue(iterator.Value().Interface())
		}
		return result
	case reflect.Slice, reflect.Array:
		result := make([]any, reflected.Len())
		for index := 0; index < reflected.Len(); index++ {
			result[index] = normalizeValue(reflected.Index(index).Interface())
		}
		return result
	default:
		return value
	}
}

func renderChange(definition harnessDefinition, scope Scope, raw []byte, server StdioServer, desired DesiredState, allowReplace bool) (ownership, []byte, string, error) {
	switch definition.format {
	case formatJSON, formatJSONC:
		return renderJSONChange(definition, raw, server, desired, allowReplace)
	case formatTOML:
		return renderTOMLChange(raw, server, desired, allowReplace)
	case formatYAMLList:
		return renderYAMLChange(raw, server, desired, allowReplace, scope.Mode == ScopeProject)
	default:
		return "", nil, "", fmt.Errorf("unsupported config format %q", definition.format)
	}
}

func renderJSONChange(definition harnessDefinition, raw []byte, server StdioServer, desired DesiredState, allowReplace bool) (ownership, []byte, string, error) {
	source := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	if len(bytes.TrimSpace(source)) == 0 {
		source = []byte("{}")
	}
	var humanJSON *hujson.Value
	humanSource := make([]byte, len(source))
	copy(humanSource, source)
	parsedSyntax, err := hujson.Parse(humanSource)
	if err != nil {
		return "", nil, "", fmt.Errorf("invalid %s configuration: %w", definition.format, err)
	}
	if definition.format == formatJSON && !parsedSyntax.IsStandard() {
		return "", nil, "", errors.New("JSON configuration contains comments or trailing commas")
	}
	if err := rejectDuplicateJSONMembers(&parsedSyntax); err != nil {
		return "", nil, "", err
	}
	if definition.format == formatJSONC {
		// hujson nodes alias their input. The dedicated humanSource copy keeps
		// planning from mutating the compare-and-swap snapshot.
		semanticSource := make([]byte, len(source))
		copy(semanticSource, source)
		standard, err := jsoncToJSON(semanticSource)
		if err != nil {
			return "", nil, "", fmt.Errorf("standardize JSONC configuration: %w", err)
		}
		humanJSON = &parsedSyntax
		source = standard
	}
	var root map[string]any
	decoder := json.NewDecoder(bytes.NewReader(source))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return "", nil, "", fmt.Errorf("invalid %s configuration: %w", definition.format, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return "", nil, "", errors.New("configuration must contain exactly one JSON value")
	}
	if root == nil {
		return "", nil, "", errors.New("configuration root must be an object")
	}
	servers := map[string]any{}
	if existing, found := root[definition.topKey]; found {
		var ok bool
		servers, ok = existing.(map[string]any)
		if !ok {
			return "", nil, "", fmt.Errorf("%s must be an object", definition.topKey)
		}
	}
	expected := serverEntry(definition.entry, server)
	current, found := servers[server.Name]
	state := ownershipAbsent
	if found {
		state = ownershipForeign
		if semanticEqual(current, expected) {
			state = ownershipOwned
		}
	}
	if state == ownershipForeign && !allowReplace {
		return state, raw, "", nil
	}
	if desired == Present {
		if state == ownershipOwned {
			return state, raw, "", nil
		}
		servers[server.Name] = expected
		root[definition.topKey] = servers
	} else {
		if state == ownershipAbsent {
			return state, raw, "", nil
		}
		delete(servers, server.Name)
		if len(servers) == 0 {
			delete(root, definition.topKey)
		} else {
			root[definition.topKey] = servers
		}
	}
	if humanJSON != nil {
		if err := mutateHumanJSON(humanJSON, definition.topKey, server.Name, expected, desired); err != nil {
			return "", nil, "", err
		}
		humanJSON.Format()
		return state, humanJSON.Pack(), actionFor(state, desired), nil
	}
	output, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", nil, "", err
	}
	return state, append(output, '\n'), actionFor(state, desired), nil
}

func rejectDuplicateJSONMembers(value *hujson.Value) error {
	switch current := value.Value.(type) {
	case *hujson.Object:
		seen := make(map[string]struct{}, len(current.Members))
		for index := range current.Members {
			literal, ok := current.Members[index].Name.Value.(hujson.Literal)
			if !ok {
				return errors.New("JSON object member name is invalid")
			}
			name := literal.String()
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("JSON object contains duplicate member %q", name)
			}
			seen[name] = struct{}{}
			if err := rejectDuplicateJSONMembers(&current.Members[index].Value); err != nil {
				return err
			}
		}
	case *hujson.Array:
		for index := range current.Elements {
			if err := rejectDuplicateJSONMembers(&current.Elements[index]); err != nil {
				return err
			}
		}
	}
	return nil
}

func jsoncToJSON(source []byte) ([]byte, error) {
	output := append([]byte(nil), source...)
	inString := false
	escaped := false
	for index := 0; index < len(output); index++ {
		character := output[index]
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		if character == '"' {
			inString = true
			continue
		}
		if character == '/' && index+1 < len(output) && output[index+1] == '/' {
			output[index], output[index+1] = ' ', ' '
			index += 2
			for index < len(output) && output[index] != '\n' && output[index] != '\r' {
				output[index] = ' '
				index++
			}
			index--
			continue
		}
		if character == '/' && index+1 < len(output) && output[index+1] == '*' {
			output[index], output[index+1] = ' ', ' '
			index += 2
			closed := false
			for index < len(output) {
				if output[index] == '*' && index+1 < len(output) && output[index+1] == '/' {
					output[index], output[index+1] = ' ', ' '
					index++
					closed = true
					break
				}
				if output[index] != '\n' && output[index] != '\r' {
					output[index] = ' '
				}
				index++
			}
			if !closed {
				return nil, errors.New("unterminated block comment")
			}
		}
	}
	if inString {
		return nil, errors.New("unterminated string")
	}
	inString, escaped = false, false
	for index := 0; index < len(output); index++ {
		character := output[index]
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		if character == '"' {
			inString = true
			continue
		}
		if character != ',' {
			continue
		}
		next := index + 1
		for next < len(output) && (output[next] == ' ' || output[next] == '\t' || output[next] == '\r' || output[next] == '\n') {
			next++
		}
		if next < len(output) && (output[next] == '}' || output[next] == ']') {
			output[index] = ' '
		}
	}
	return output, nil
}

func mutateHumanJSON(document *hujson.Value, topKey, name string, expected map[string]any, desired DesiredState) error {
	root, ok := document.Value.(*hujson.Object)
	if !ok {
		return errors.New("JSONC configuration root must be an object")
	}
	topIndex := humanJSONMember(root, topKey)
	if desired == Present && topIndex < 0 {
		value, err := humanJSONValue(map[string]any{name: expected})
		if err != nil {
			return err
		}
		root.Members = append(root.Members, hujson.ObjectMember{
			Name:  hujson.Value{Value: hujson.String(topKey)},
			Value: value,
		})
		return nil
	}
	if topIndex < 0 {
		return nil
	}
	servers, ok := root.Members[topIndex].Value.Value.(*hujson.Object)
	if !ok {
		return fmt.Errorf("%s must be an object", topKey)
	}
	serverIndex := humanJSONMember(servers, name)
	if desired == Present {
		value, err := humanJSONValue(expected)
		if err != nil {
			return err
		}
		if serverIndex < 0 {
			servers.Members = append(servers.Members, hujson.ObjectMember{
				Name:  hujson.Value{Value: hujson.String(name)},
				Value: value,
			})
		} else {
			servers.Members[serverIndex].Value.Value = value.Value
		}
		return nil
	}
	if serverIndex < 0 {
		return nil
	}
	servers.Members = append(servers.Members[:serverIndex], servers.Members[serverIndex+1:]...)
	if len(servers.Members) == 0 {
		root.Members = append(root.Members[:topIndex], root.Members[topIndex+1:]...)
	}
	return nil
}

func humanJSONMember(object *hujson.Object, name string) int {
	for index, member := range object.Members {
		literal, ok := member.Name.Value.(hujson.Literal)
		if ok && literal.String() == name {
			return index
		}
	}
	return -1
}

func humanJSONValue(value any) (hujson.Value, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return hujson.Value{}, err
	}
	parsed, err := hujson.Parse(raw)
	if err != nil {
		return hujson.Value{}, fmt.Errorf("build JSONC value: %w", err)
	}
	return parsed, nil
}

func renderTOMLChange(raw []byte, server StdioServer, desired DesiredState, allowReplace bool) (ownership, []byte, string, error) {
	source := bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	document := map[string]any{}
	if len(bytes.TrimSpace(source)) > 0 {
		if err := toml.Unmarshal(source, &document); err != nil {
			return "", nil, "", fmt.Errorf("invalid TOML configuration: %w", err)
		}
	}
	servers := map[string]any{}
	if current, found := document["mcp_servers"]; found {
		var ok bool
		servers, ok = current.(map[string]any)
		if !ok {
			return "", nil, "", errors.New("mcp_servers must be a table")
		}
	}
	expected := standardEntry(server)
	current, found := servers[server.Name]
	state := ownershipAbsent
	if found {
		state = ownershipForeign
		if semanticEqual(current, expected) {
			state = ownershipOwned
		}
	}
	if state == ownershipForeign && !allowReplace {
		return state, raw, "", nil
	}
	if desired == Present && state == ownershipOwned || desired == Absent && state == ownershipAbsent {
		return state, raw, "", nil
	}
	eol := "\n"
	if bytes.Contains(source, []byte("\r\n")) {
		eol = "\r\n"
	}
	output := append([]byte(nil), source...)
	if state != ownershipAbsent {
		ranges, err := tomlServerRanges(string(source), server.Name)
		if err != nil {
			return "", nil, "", err
		}
		for index := len(ranges) - 1; index >= 0; index-- {
			span := ranges[index]
			output = append(output[:span.start], output[span.end:]...)
		}
	}
	if desired == Present {
		separator := ""
		if len(output) > 0 {
			separator = eol
			if !bytes.HasSuffix(output, []byte("\n")) {
				separator = eol + eol
			}
		}
		output = append(output, []byte(separator+tomlBlock(server, eol))...)
	}
	verified := map[string]any{}
	if len(bytes.TrimSpace(output)) > 0 {
		if err := toml.Unmarshal(output, &verified); err != nil {
			return "", nil, "", fmt.Errorf("generated TOML failed validation: %w", err)
		}
	}
	return state, output, actionFor(state, desired), nil
}

func tomlBlock(server StdioServer, eol string) string {
	lines := []string{
		"[mcp_servers." + quote(server.Name) + "]",
		"command = " + quote(server.Command),
	}
	arguments := make([]string, len(server.Args))
	for index, argument := range server.Args {
		arguments[index] = quote(argument)
	}
	lines = append(lines, "args = ["+strings.Join(arguments, ", ")+"]")
	if len(server.Env) > 0 {
		keys := make([]string, 0, len(server.Env))
		for key := range server.Env {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		values := make([]string, 0, len(keys))
		for _, key := range keys {
			values = append(values, quote(key)+" = "+quote(server.Env[key]))
		}
		lines = append(lines, "env = { "+strings.Join(values, ", ")+" }")
	}
	return strings.Join(lines, eol) + eol
}

func quote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

type sourceRange struct{ start, end int }

type tomlHeader struct {
	start   int
	lineEnd int
	path    []string
	array   bool
}

func tomlServerRanges(raw, name string) ([]sourceRange, error) {
	headers := scanTOMLHeaders(raw)
	target := []string{"mcp_servers", name}
	var ranges []sourceRange
	baseFound := false
	for index, header := range headers {
		if header.path == nil {
			return nil, errors.New("could not safely parse a TOML table header")
		}
		if !hasPathPrefix(header.path, target) {
			continue
		}
		if len(header.path) == len(target) && !header.array {
			baseFound = true
		}
		end := len(raw)
		if index+1 < len(headers) {
			end = preserveFollowingComments(raw, header.lineEnd, headers[index+1].start)
		}
		ranges = append(ranges, sourceRange{start: header.start, end: end})
	}
	if !baseFound || len(ranges) == 0 {
		return nil, errors.New("parsed MCP server table could not be located safely in TOML source")
	}
	return ranges, nil
}

func scanTOMLHeaders(raw string) []tomlHeader {
	var headers []tomlHeader
	for start := 0; start < len(raw); {
		newline := strings.IndexByte(raw[start:], '\n')
		end := len(raw)
		if newline >= 0 {
			end = start + newline + 1
		}
		line := strings.TrimSpace(strings.TrimSuffix(raw[start:end], "\n"))
		if strings.HasPrefix(line, "[") {
			array := strings.HasPrefix(line, "[[")
			if parsed := parseTOMLHeader(line); parsed != nil {
				headers = append(headers, tomlHeader{start: start, lineEnd: end, path: parsed, array: array})
			} else {
				headers = append(headers, tomlHeader{start: start, lineEnd: end, array: array})
			}
		}
		start = end
	}
	return headers
}

func parseTOMLHeader(line string) []string {
	document := map[string]any{}
	if err := toml.Unmarshal([]byte(line+"\n__detect_harness_marker = true\n"), &document); err != nil {
		return nil
	}
	return markerPath(document, nil)
}

func markerPath(value any, prefix []string) []string {
	if mapping, ok := value.(map[string]any); ok {
		if marker, exists := mapping["__detect_harness_marker"]; exists && marker == true {
			return prefix
		}
		for key, child := range mapping {
			if found := markerPath(child, append(append([]string(nil), prefix...), key)); found != nil {
				return found
			}
		}
	}
	if sequence, ok := value.([]any); ok {
		for _, child := range sequence {
			if found := markerPath(child, prefix); found != nil {
				return found
			}
		}
	}
	return nil
}

func hasPathPrefix(path, prefix []string) bool {
	if len(path) < len(prefix) {
		return false
	}
	for index := range prefix {
		if path[index] != prefix[index] {
			return false
		}
	}
	return true
}

func preserveFollowingComments(raw string, start, end int) int {
	segment := raw[start:end]
	lines := strings.SplitAfter(segment, "\n")
	offset := len(segment)
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.TrimSpace(lines[index])
		if line != "" && !strings.HasPrefix(line, "#") {
			break
		}
		offset -= len(lines[index])
	}
	return start + offset
}

func renderYAMLChange(raw []byte, server StdioServer, desired DesiredState, allowReplace bool, bare bool) (ownership, []byte, string, error) {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF})
	document := &yaml.Node{}
	fresh := len(bytes.TrimSpace(raw)) == 0
	if fresh {
		document.Kind = yaml.DocumentNode
		document.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	} else {
		decoder := yaml.NewDecoder(bytes.NewReader(raw))
		if err := decoder.Decode(document); err != nil {
			return "", nil, "", fmt.Errorf("invalid YAML configuration: %w", err)
		}
		var trailing yaml.Node
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return "", nil, "", errors.New("multiple YAML documents are not supported")
			}
			return "", nil, "", fmt.Errorf("invalid trailing YAML document: %w", err)
		}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return "", nil, "", errors.New("YAML configuration root must be a map")
	}
	root := document.Content[0]
	if fresh && desired == Present && !bare {
		setYAMLScalar(root, "name", "Local Config")
		setYAMLScalar(root, "version", "0.0.1")
		setYAMLScalar(root, "schema", "v1")
	}
	keyIndex := yamlMapKey(root, "mcpServers")
	var sequence *yaml.Node
	if keyIndex >= 0 {
		sequence = root.Content[keyIndex+1]
		if sequence.Kind != yaml.SequenceNode {
			return "", nil, "", errors.New("mcpServers must be a sequence")
		}
	} else {
		sequence = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	}
	expected := continueEntry(server)
	matching := make([]int, 0, 1)
	owned := false
	for index, item := range sequence.Content {
		var value map[string]any
		if err := item.Decode(&value); err != nil {
			continue
		}
		if value["name"] == server.Name {
			matching = append(matching, index)
			if len(matching) == 1 && semanticEqual(value, expected) {
				owned = true
			}
		}
	}
	state := ownershipAbsent
	if len(matching) > 0 {
		state = ownershipForeign
		if len(matching) == 1 && owned {
			state = ownershipOwned
		}
	}
	if state == ownershipForeign && !allowReplace {
		return state, raw, "", nil
	}
	if desired == Present && state == ownershipOwned || desired == Absent && state == ownershipAbsent {
		return state, raw, "", nil
	}
	filtered := make([]*yaml.Node, 0, len(sequence.Content)+1)
	for index, item := range sequence.Content {
		if !containsIndex(matching, index) {
			filtered = append(filtered, item)
		}
	}
	sequence.Content = filtered
	if desired == Present {
		node := &yaml.Node{}
		if err := node.Encode(expected); err != nil {
			return "", nil, "", err
		}
		sequence.Content = append(sequence.Content, node)
		if keyIndex < 0 {
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "mcpServers"}, sequence)
		}
	} else if len(sequence.Content) == 0 && keyIndex >= 0 {
		root.Content = append(root.Content[:keyIndex], root.Content[keyIndex+2:]...)
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(document); err != nil {
		return "", nil, "", err
	}
	_ = encoder.Close()
	return state, output.Bytes(), actionFor(state, desired), nil
}

func continueEntry(server StdioServer) map[string]any {
	entry := map[string]any{
		"name":    server.Name,
		"type":    "stdio",
		"command": server.Command,
		"args":    append([]string(nil), server.Args...),
	}
	if len(server.Env) > 0 {
		entry["env"] = cloneEnv(server.Env)
	}
	return entry
}

func yamlMapKey(mapping *yaml.Node, key string) int {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return index
		}
	}
	return -1
}

func setYAMLScalar(mapping *yaml.Node, key, value string) {
	if yamlMapKey(mapping, key) >= 0 {
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func containsIndex(indices []int, candidate int) bool {
	for _, index := range indices {
		if index == candidate {
			return true
		}
	}
	return false
}

func actionFor(state ownership, desired DesiredState) string {
	if desired == Absent {
		return "remove"
	}
	if state == ownershipAbsent {
		return "add"
	}
	return "update"
}

// RenderConfig generates a complete global configuration containing only server
// for one harness. It does not inspect or mutate the filesystem.
func RenderConfig(id ID, server StdioServer) (string, error) {
	return RenderConfigScoped(id, server, Scope{})
}

// RenderConfigScoped generates a complete configuration containing only server
// for one harness in the requested scope. For project scope it targets the
// harness's directory-local file; the directory itself is not inspected or
// created. An unsupported project scope returns an error.
func RenderConfigScoped(id ID, server StdioServer, scope Scope) (string, error) {
	if err := server.validate(); err != nil {
		return "", err
	}
	normalized, err := scope.normalize()
	if err != nil {
		return "", err
	}
	definition, found := definitionFor(id)
	if !found {
		return "", fmt.Errorf("unknown harness %q", id)
	}
	if normalized.Mode == ScopeProject && definition.project == nil {
		return "", fmt.Errorf("%s has no project-scope configuration", definition.Name)
	}
	_, output, _, err := renderChange(definition, normalized, nil, server, Present, false)
	return string(output), err
}
