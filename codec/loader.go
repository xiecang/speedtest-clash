package codec

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var (
	// ErrNoNodes indicates a valid source generation contained zero supported proxy nodes.
	ErrNoNodes = errors.New("source contains no supported nodes")

	errSourceBudget  = errors.New("source resource budget exhausted")
	errSourceStorage = errors.New("source spool storage failed")
)

type SourceRequest struct {
	Source          string `json:"source"`
	AllowLocalFiles bool   `json:"allow_local_files,omitempty"`
	UserAgent       string `json:"user_agent,omitempty"`
	Proxy           string `json:"proxy,omitempty"`
}

type SourceResult struct {
	Index   int         `json:"index"`
	Error   string      `json:"error,omitempty"`
	Headers http.Header `json:"headers,omitempty"`
	Bytes   int         `json:"bytes"`
}

type SourceSummary struct {
	Partial     bool           `json:"partial,omitempty"`
	Total       int            `json:"total"`
	Supported   int            `json:"supported"`
	Unsupported int            `json:"unsupported"`
	Diagnostics []Diagnostic   `json:"diagnostics,omitempty"`
	Sources     []SourceResult `json:"sources,omitempty"`
}

// ClashUserAgent ensures subscription HTTP fetches advertise ClashMeta compatibility
// while preserving caller-specified User-Agent tokens.
func ClashUserAgent(preferred string) string {
	preferred = strings.TrimSpace(preferred)
	if preferred == "" {
		return "ClashMeta"
	}
	if strings.Contains(strings.ToLower(preferred), "clash") {
		return preferred
	}
	return preferred + " ClashMeta"
}

// LoadSource loads a single source (or |-joined multi-source list) strictly:
// any unsupported node, broken dependency, or failed provider aborts before emitting.
func LoadSource(ctx context.Context, source string, client *http.Client, emit func([]Node) error) error {
	requests := expandPipeSources(SourceRequest{Source: source, AllowLocalFiles: true})
	_, err := loadSourcesInternal(ctx, requests, client, emit, false, true)
	return err
}

// LoadSourceDetailed loads a single source (or |-joined multi-source list), isolating
// unsupported entries and failed sub-providers while validating surviving dependencies.
func LoadSourceDetailed(ctx context.Context, source string, client *http.Client, emit func([]Node) error) (SourceSummary, error) {
	requests := expandPipeSources(SourceRequest{Source: source, AllowLocalFiles: true})
	return loadSourcesInternal(ctx, requests, client, emit, len(requests) > 1, false)
}

// LoadSourceRequestDetailed applies SourceRequest options and isolates failed sub-providers.
func LoadSourceRequestDetailed(ctx context.Context, request SourceRequest, client *http.Client, emit func([]Node) error) (SourceSummary, error) {
	requests := expandPipeSources(request)
	return loadSourcesInternal(ctx, requests, client, emit, len(requests) > 1, false)
}

// LoadSourcesDetailed loads multiple ordered SourceRequests, rolling back failed sibling
// sources while keeping healthy siblings intact.
func LoadSourcesDetailed(ctx context.Context, requests []SourceRequest, client *http.Client, emit func([]Node) error) (SourceSummary, error) {
	return loadSourcesInternal(ctx, requests, client, emit, true, false)
}

func expandPipeSources(req SourceRequest) []SourceRequest {
	trimmed := strings.TrimSpace(req.Source)
	if isInlineOrDataSource(trimmed) || !strings.Contains(trimmed, "|") {
		return []SourceRequest{req}
	}
	parts := strings.Split(trimmed, "|")
	out := make([]SourceRequest, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		sub := req
		sub.Source = part
		out = append(out, sub)
	}
	if len(out) == 0 {
		return []SourceRequest{req}
	}
	return out
}

type sourceSlot struct {
	offset                int64
	size                  int
	name, dependency, key string
	format                Format
	state                 int
	valid                 bool
	hidden                bool
}

type sourceSpool struct {
	file    *os.File
	slots   []sourceSlot
	names   map[string][]int
	bytes   int64
	entries int
	summary SourceSummary
	strict  bool
	err     error
}

func loadSourcesInternal(ctx context.Context, requests []SourceRequest, client *http.Client, emit func([]Node) error, partial, strict bool) (SourceSummary, error) {
	if len(requests) == 0 || len(requests) > 64 {
		return SourceSummary{}, errors.New("expected 1..64 sources")
	}
	file, err := os.CreateTemp("", "node-quality-source-*")
	if err != nil {
		return SourceSummary{}, err
	}
	defer os.Remove(file.Name())
	defer file.Close()

	spool := sourceSpool{file: file, names: map[string][]int{}, strict: strict}
	totalBytes, allowEmpty := 0, false

	for index, request := range requests {
		if err := ctx.Err(); err != nil {
			return spool.summary, err
		}
		result := SourceResult{Index: index, Headers: http.Header{}}
		source := strings.TrimSpace(request.Source)
		if source == "empty" && !strict {
			allowEmpty = true
			spool.summary.Sources = append(spool.summary.Sources, result)
			continue
		}
		c, closeTransport, err := buildSourceClient(client, request, &result)
		if err != nil {
			return spool.summary, err
		}
		start, offset, before := len(spool.slots), spool.bytes, spool.summary
		visited := map[string]bool{}

		var load func(string, int, string) error
		load = func(address string, depth int, path string) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !request.AllowLocalFiles && !isRemoteHTTPURL(address) && !isInlineOrDataSource(address) {
				return errors.New("local source files are disabled")
			}
			if !isRemoteHTTPURL(address) && !isInlineOrDataSource(address) {
				address = filepath.Clean(address)
			}
			if depth > 8 || visited[address] {
				return errors.New("provider depth or cycle limit exceeded")
			}
			visited[address] = true
			defer delete(visited, address)

			content, err := readSourceContent(ctx, address, c)
			totalBytes += len(content)
			result.Bytes += len(content)
			if totalBytes > MaxSourceBytes {
				return fmt.Errorf("%w: sources and providers exceed 16MiB", errSourceBudget)
			}
			if err != nil {
				return err
			}
			providers, err := spool.decode(content)
			if err != nil {
				return err
			}
			if strict && spool.summary.Unsupported > 0 {
				msg := "unsupported entry"
				if len(spool.summary.Diagnostics) > 0 {
					msg = spool.summary.Diagnostics[0].Message
				}
				return fmt.Errorf("source has %d unsupported entries; first reason: %s", spool.summary.Unsupported, msg)
			}

			names := make([]string, 0, len(providers))
			for name := range providers {
				names = append(names, name)
			}
			sort.Strings(names)
			for childIndex, name := range names {
				child, childErr := resolveProviderAddress(address, providers[name])
				childPath := fmt.Sprintf("%s/%d", path, childIndex)
				cStart, cOffset, cBefore := len(spool.slots), spool.bytes, spool.summary
				if childErr == nil {
					childErr = load(child, depth+1, childPath)
				}
				if childErr != nil {
					if strict || fatalSourceError(ctx, childErr) {
						return childErr
					}
					if rbErr := spool.rollback(cStart, cOffset); rbErr != nil {
						return rbErr
					}
					spool.summary = cBefore
					spool.sourceFailure(childPath, "provider_failed", childErr.Error())
				}
			}
			if strict && spool.summary.Partial {
				return errors.New("provider load failed")
			}
			return nil
		}

		err = load(source, 0, fmt.Sprint(index))
		closeTransport()
		if err != nil {
			if strict || !partial || fatalSourceError(ctx, err) {
				return spool.summary, err
			}
			if rbErr := spool.rollback(start, offset); rbErr != nil {
				return spool.summary, rbErr
			}
			spool.summary = before
			result.Error = err.Error()
			result.Headers = nil
			spool.sourceFailure(fmt.Sprint(index), "source_failed", result.Error)
		}
		spool.summary.Sources = append(spool.summary.Sources, result)
	}
	return spool.finish(ctx, emit, allowEmpty)
}

func (spool *sourceSpool) finish(ctx context.Context, emit func([]Node) error, allowEmpty bool) (SourceSummary, error) {
	for i := range spool.slots {
		if err := ctx.Err(); err != nil {
			return spool.summary, err
		}
		spool.resolve(i, 0)
		if spool.err != nil {
			return spool.summary, spool.err
		}
	}
	if spool.strict && spool.summary.Unsupported > 0 {
		return spool.summary, errors.New("proxy dependency resolution failed")
	}
	spool.summary.Supported = 0
	for _, slot := range spool.slots {
		if slot.valid {
			spool.summary.Supported++
		}
	}
	if spool.summary.Supported == 0 && (!allowEmpty || spool.summary.Partial) {
		if spool.summary.Partial {
			return spool.summary, errors.New("incomplete source has no supported nodes")
		}
		return spool.summary, ErrNoNodes
	}
	if err := spool.emit(ctx, emit); err != nil {
		return spool.summary, err
	}
	return spool.summary, nil
}

func readSourceContent(ctx context.Context, source string, client *http.Client) ([]byte, error) {
	if strings.HasPrefix(source, "inline:data:") {
		source = strings.TrimPrefix(source, "inline:")
	}
	var reader io.ReadCloser
	switch {
	case strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://"):
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if err != nil {
			return nil, errors.New("invalid source URL")
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, errors.New("source fetch failed")
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("source HTTP status %d", response.StatusCode)
		}
		reader = response.Body
	case strings.HasPrefix(source, "data:"):
		header, data, ok := strings.Cut(strings.TrimPrefix(source, "data:"), ",")
		if !ok {
			return nil, errors.New("invalid data URL")
		}
		decoded, err := url.PathUnescape(data)
		if err != nil {
			return nil, errors.New("invalid data URL escape")
		}
		if strings.HasSuffix(strings.ToLower(header), ";base64") {
			raw, e := base64.StdEncoding.DecodeString(decoded)
			if e != nil {
				return nil, errors.New("invalid data URL base64")
			}
			decoded = string(raw)
		}
		reader = io.NopCloser(strings.NewReader(decoded))
	case strings.HasPrefix(source, "inline:"):
		reader = io.NopCloser(strings.NewReader(strings.TrimPrefix(source, "inline:")))
	default:
		file, err := os.Open(source)
		if err != nil {
			return nil, errors.New("source file open failed")
		}
		reader = file
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, MaxSourceBytes+1))
	if err != nil {
		return content, errors.New("source read failed")
	}
	if len(content) > MaxSourceBytes {
		return content, fmt.Errorf("%w: source exceeds 16MiB", errSourceBudget)
	}
	return content, nil
}

func (s *sourceSpool) diagnostic(code, message string) {
	s.summary.Unsupported++
	if message == "" {
		message = code
	}
	if len(s.summary.Diagnostics) < 100 {
		s.summary.Diagnostics = append(s.summary.Diagnostics, Diagnostic{
			Node:    fmt.Sprint(s.summary.Total),
			Code:    code,
			Message: message,
		})
	}
}

func (s *sourceSpool) countEntry() error {
	s.summary.Total++
	s.entries++
	if s.entries > 50000 {
		return fmt.Errorf("%w: source exceeds 50000 entries", errSourceBudget)
	}
	return nil
}

func (s *sourceSpool) add(config map[string]any, format Format) error {
	if err := s.countEntry(); err != nil {
		return err
	}
	node, err := NewNode(config, format)
	if err != nil {
		s.diagnostic("invalid_node", err.Error())
		return nil
	}
	kind, _ := node.Config["type"].(string)
	if !supportedSourceKind(kind, format) {
		s.diagnostic("unsupported_type", fmt.Sprintf("unsupported protocol %q", kind))
		return nil
	}
	content, err := json.Marshal(node.Config)
	if err != nil {
		return err
	}
	if s.bytes+int64(len(content)) > 64<<20 {
		return fmt.Errorf("%w: source spool exceeds 64MiB", errSourceBudget)
	}
	slot := sourceSlot{
		offset:     s.bytes,
		size:       len(content),
		name:       node.Name,
		key:        node.Key,
		format:     format,
		valid:      true,
		hidden:     node.Hidden,
		dependency: node.DialerRef(),
	}
	if _, err = s.file.Write(content); err != nil {
		return fmt.Errorf("%w: %w", errSourceStorage, err)
	}
	s.bytes += int64(len(content))
	s.names[slot.name] = append(s.names[slot.name], len(s.slots))
	s.slots = append(s.slots, slot)
	return nil
}

func (s *sourceSpool) decode(content []byte) (map[string]string, error) {
	content = trimSourceContent(content)
	if len(content) == 0 {
		return nil, errors.New("empty node source")
	}
	if looksLikeBase64(content) {
		compact := stripASCIIWhitespace(content)
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
			buf := make([]byte, enc.DecodedLen(len(compact)))
			if n, err := enc.Decode(buf, compact); err == nil && n > 0 {
				content = trimSourceContent(buf[:n])
				break
			}
		}
	}
	if bytes.HasPrefix(content, []byte("{")) {
		if !json.Valid(content) {
			return s.decodeYAML(content)
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(content, &doc); err != nil {
			return nil, errors.New("invalid JSON source")
		}
		if doc["outbounds"] != nil || doc["endpoints"] != nil {
			for _, field := range []string{"outbounds", "endpoints"} {
				var entries []json.RawMessage
				if err := json.Unmarshal(doc[field], &entries); err != nil && doc[field] != nil {
					return nil, errors.New("invalid native source entries")
				}
				for _, raw := range entries {
					var config map[string]any
					if json.Unmarshal(raw, &config) != nil || config == nil {
						if err := s.countEntry(); err != nil {
							return nil, err
						}
						s.diagnostic("invalid_node", "invalid native entry")
						continue
					}
					kind, _ := config["type"].(string)
					if kind == "selector" || kind == "urltest" || kind == "block" || kind == "dns" {
						continue
					}
					if err := s.add(config, SingBoxJSON); err != nil {
						return nil, err
					}
				}
			}
			return nil, nil
		}
	}
	if sourceURIList(content) {
		for _, line := range strings.Split(string(content), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			var config map[string]any
			var metadata bool
			var err error
			if s.strict {
				config, err = DecodeURI(line)
			} else {
				config, metadata, err = sourceURIConfig(line)
			}
			if err != nil {
				if err := s.countEntry(); err != nil {
					return nil, err
				}
				s.diagnostic("unsupported_uri", err.Error())
				continue
			}
			if err = s.add(config, ClashYAML); err != nil {
				return nil, err
			}
			if metadata && len(s.summary.Diagnostics) < 100 {
				s.summary.Diagnostics = append(s.summary.Diagnostics, Diagnostic{
					Code:    "legacy_uri_metadata",
					Message: "URI converter stripped unapplied spx/ech hints",
				})
			}
		}
		return nil, nil
	}
	return s.decodeYAML(content)
}

func (s *sourceSpool) decodeYAML(content []byte) (map[string]string, error) {
	if len(content) > 256<<10 && !bytes.HasPrefix(content, []byte("{")) && !bytes.Contains(content, []byte("&")) {
		if providers, ok, err := s.decodeYAMLChunked(content); ok {
			return providers, err
		}
	}
	return s.decodeYAMLDocument(content)
}

func (s *sourceSpool) decodeYAMLDocument(content []byte) (map[string]string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return nil, errors.New("invalid YAML source")
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("unrecognized node source")
	}
	providers := map[string]string{}
	root := doc.Content[0]
	seenKeys := make(map[string]bool, len(root.Content)/2)
	found := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		field, value := root.Content[i].Value, root.Content[i+1]
		if seenKeys[field] {
			return nil, errors.New("duplicate YAML field")
		}
		seenKeys[field] = true
		switch field {
		case "proxies", "Proxy":
			found = true
			if err := s.decodeYAMLProxyNodes(value); err != nil {
				return nil, err
			}
		case "proxy-providers":
			found = true
			var p any
			if err := value.Decode(&p); err != nil {
				return nil, errors.New("invalid providers")
			}
			providers = s.providerURLs(p)
		default:
			root.Content[i+1] = nil
		}
	}
	if !found {
		return nil, errors.New("source contains no proxy fields")
	}
	return providers, nil
}

func (s *sourceSpool) decodeYAMLProxyNodes(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode && (value.Tag == "!!null" || value.Value == "" || value.Value == "null") {
		return nil
	}
	if value.Kind != yaml.SequenceNode {
		return errors.New("proxies must be an array")
	}
	for j, item := range value.Content {
		if item.Kind != yaml.MappingNode {
			if err := s.countEntry(); err != nil {
				return err
			}
			s.diagnostic("invalid_node", "proxy entry must be a mapping")
			value.Content[j] = nil
			continue
		}
		var config map[string]any
		if err := item.Decode(&config); err != nil || config == nil {
			if err := s.countEntry(); err != nil {
				return err
			}
			s.diagnostic("invalid_node", "invalid YAML proxy entry")
			value.Content[j] = nil
			continue
		}
		if err := s.add(config, ClashYAML); err != nil {
			return err
		}
		value.Content[j] = nil
	}
	return nil
}

type yamlTopSection struct {
	name         string
	inlineValue  bool
	sectionStart int
	bodyStart    int
	bodyEnd      int
}

func (s *sourceSpool) decodeYAMLChunked(content []byte) (map[string]string, bool, error) {
	var sections []yamlTopSection
	flowDepth := 0
	inSingle, inDouble := false, false
	pos := 0
	for pos < len(content) {
		lineStart := pos
		nl := bytes.IndexByte(content[pos:], '\n')
		var lineEnd, nextPos int
		if nl < 0 {
			lineEnd = len(content)
			nextPos = len(content)
		} else {
			lineEnd = pos + nl
			nextPos = lineEnd + 1
		}
		line := content[lineStart:lineEnd]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		trimmed := bytes.TrimSpace(line)
		if flowDepth == 0 && !inSingle && !inDouble && len(trimmed) > 0 && trimmed[0] != '#' {
			if bytes.Equal(trimmed, []byte("---")) || bytes.Equal(trimmed, []byte("...")) {
				if len(sections) > 0 {
					return nil, false, nil
				}
				pos = nextPos
				continue
			}
			if line[0] != ' ' && line[0] != '\t' {
				isSeqItem := line[0] == '-' && (len(line) == 1 || line[1] == ' ' || line[1] == '\t')
				if !isSeqItem {
					key, rest, ok := parseTopLevelYAMLKey(line)
					if !ok {
						return nil, false, nil
					}
					if len(sections) > 0 {
						sections[len(sections)-1].bodyEnd = lineStart
					}
					restTrim := bytes.TrimSpace(rest)
					hasInline := len(restTrim) > 0 && restTrim[0] != '#'
					sections = append(sections, yamlTopSection{
						name:         key,
						inlineValue:  hasInline,
						sectionStart: lineStart,
						bodyStart:    nextPos,
						bodyEnd:      len(content),
					})
					updateYAMLFlowState(rest, &flowDepth, &inSingle, &inDouble)
					pos = nextPos
					continue
				} else if len(sections) == 0 {
					return nil, false, nil
				}
			}
		}
		updateYAMLFlowState(line, &flowDepth, &inSingle, &inDouble)
		pos = nextPos
	}
	if len(sections) == 0 {
		return nil, false, nil
	}
	if flowDepth != 0 || inSingle || inDouble {
		return nil, true, errors.New("invalid YAML source")
	}

	seenKeys := make(map[string]bool, len(sections))
	providers := map[string]string{}
	found := false
	var chunkBuf bytes.Buffer

	for _, sec := range sections {
		if seenKeys[sec.name] {
			return nil, true, errors.New("duplicate YAML field")
		}
		seenKeys[sec.name] = true
		rawSection := content[sec.sectionStart:sec.bodyEnd]
		switch sec.name {
		case "proxies", "Proxy":
			found = true
			if sec.inlineValue || len(rawSection) <= 256<<10 {
				var doc yaml.Node
				if err := yaml.Unmarshal(rawSection, &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || len(doc.Content[0].Content) < 2 {
					return nil, true, errors.New("invalid YAML source")
				}
				if err := s.decodeYAMLProxyNodes(doc.Content[0].Content[1]); err != nil {
					return nil, true, err
				}
				continue
			}
			if err := streamYAMLSequenceChunks(content[sec.bodyStart:sec.bodyEnd], &chunkBuf, "proxies:\n", 200, func(chunk []byte) error {
				var doc yaml.Node
				if err := yaml.Unmarshal(chunk, &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || len(doc.Content[0].Content) < 2 {
					return errors.New("invalid YAML source")
				}
				return s.decodeYAMLProxyNodes(doc.Content[0].Content[1])
			}); err != nil {
				return nil, true, err
			}
		case "proxy-providers":
			found = true
			var doc yaml.Node
			if err := yaml.Unmarshal(rawSection, &doc); err != nil || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode || len(doc.Content[0].Content) < 2 {
				return nil, true, errors.New("invalid YAML source")
			}
			var p any
			if err := doc.Content[0].Content[1].Decode(&p); err != nil {
				return nil, true, errors.New("invalid providers")
			}
			providers = s.providerURLs(p)
		default:
			if sec.inlineValue || len(rawSection) <= 256<<10 {
				var check yaml.Node
				if err := yaml.Unmarshal(rawSection, &check); err != nil {
					return nil, true, errors.New("invalid YAML source")
				}
				continue
			}
			if err := streamYAMLSequenceChunks(content[sec.bodyStart:sec.bodyEnd], &chunkBuf, "v:\n", 1000, func(chunk []byte) error {
				var check yaml.Node
				if err := yaml.Unmarshal(chunk, &check); err != nil {
					return errors.New("invalid YAML source")
				}
				return nil
			}); err != nil {
				return nil, true, err
			}
		}
	}
	if !found {
		return nil, true, errors.New("source contains no proxy fields")
	}
	return providers, true, nil
}

func streamYAMLSequenceChunks(body []byte, buf *bytes.Buffer, header string, batchItems int, handle func([]byte) error) error {
	seqIndent := -1
	flowDepth := 0
	inSingle, inDouble := false, false
	chunkStart := 0
	items := 0
	pos := 0
	flush := func(end int) error {
		if items == 0 && len(bytes.TrimSpace(body[chunkStart:end])) == 0 {
			chunkStart = end
			return nil
		}
		buf.Reset()
		buf.WriteString(header)
		buf.Write(body[chunkStart:end])
		chunkStart = end
		items = 0
		return handle(buf.Bytes())
	}
	for pos < len(body) {
		lineStart := pos
		nl := bytes.IndexByte(body[pos:], '\n')
		var lineEnd, nextPos int
		if nl < 0 {
			lineEnd = len(body)
			nextPos = len(body)
		} else {
			lineEnd = pos + nl
			nextPos = lineEnd + 1
		}
		line := body[lineStart:lineEnd]
		if len(line) > 0 && line[len(line)-1] == '\r' {
			line = line[:len(line)-1]
		}
		if flowDepth == 0 && !inSingle && !inDouble {
			indent := 0
			for indent < len(line) && line[indent] == ' ' {
				indent++
			}
			if indent < len(line) && line[indent] != '#' && line[indent] != '\t' {
				isDash := line[indent] == '-' && (indent+1 == len(line) || line[indent+1] == ' ' || line[indent+1] == '\t')
				if seqIndent < 0 {
					if !isDash {
						// Non-sequence mapping body; validate whole body in one pass.
						buf.Reset()
						buf.WriteString(header)
						buf.Write(body)
						return handle(buf.Bytes())
					}
					seqIndent = indent
				}
				if indent == seqIndent && isDash {
					if items >= batchItems {
						if err := flush(lineStart); err != nil {
							return err
						}
					}
					items++
				}
			}
		}
		updateYAMLFlowState(line, &flowDepth, &inSingle, &inDouble)
		pos = nextPos
	}
	if flowDepth != 0 || inSingle || inDouble {
		return errors.New("invalid YAML source")
	}
	return flush(len(body))
}

func parseTopLevelYAMLKey(line []byte) (string, []byte, bool) {
	if len(line) == 0 {
		return "", nil, false
	}
	if line[0] == '"' || line[0] == '\'' {
		quote := line[0]
		i := 1
		for i < len(line) {
			if quote == '"' && line[i] == '\\' {
				i += 2
				continue
			}
			if line[i] == quote {
				key := string(line[1:i])
				rest := bytes.TrimLeft(line[i+1:], " \t")
				if len(rest) == 0 || rest[0] != ':' || (len(rest) > 1 && rest[1] != ' ' && rest[1] != '\t' && rest[1] != '\r') {
					return "", nil, false
				}
				return key, rest[1:], true
			}
			i++
		}
		return "", nil, false
	}
	i := 0
	for i < len(line) {
		c := line[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			i++
			continue
		}
		break
	}
	if i == 0 {
		return "", nil, false
	}
	key := string(line[:i])
	rest := bytes.TrimLeft(line[i:], " \t")
	if len(rest) == 0 || rest[0] != ':' || (len(rest) > 1 && rest[1] != ' ' && rest[1] != '\t' && rest[1] != '\r') {
		return "", nil, false
	}
	return key, rest[1:], true
}

func updateYAMLFlowState(line []byte, flowDepth *int, inSingle, inDouble *bool) {
	for i := 0; i < len(line); i++ {
		c := line[i]
		if *inSingle {
			if c == '\'' {
				if i+1 < len(line) && line[i+1] == '\'' {
					i++
				} else {
					*inSingle = false
				}
			}
			continue
		}
		if *inDouble {
			if c == '\\' {
				i++
				continue
			}
			if c == '"' {
				*inDouble = false
			}
			continue
		}
		switch c {
		case '#':
			if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
				return
			}
		case '\'':
			*inSingle = true
		case '"':
			*inDouble = true
		case '{', '[':
			*flowDepth++
		case '}', ']':
			if *flowDepth > 0 {
				*flowDepth--
			} else {
				*flowDepth = -1
				return
			}
		}
	}
}

func (s *sourceSpool) node(i int) (Node, error) {
	slot := s.slots[i]
	raw := make([]byte, slot.size)
	if _, err := s.file.ReadAt(raw, slot.offset); err != nil {
		return Node{}, err
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return Node{}, err
	}
	n := Node{
		Key:    slot.key,
		Name:   slot.name,
		Hidden: slot.hidden,
		Format: slot.format,
		Config: config,
	}
	if slot.dependency != "" {
		indices := s.names[slot.dependency]
		if len(indices) > 0 {
			n.Dependencies = []string{s.slots[indices[0]].key}
		}
	}
	return n, nil
}

func (s *sourceSpool) resolve(i, depth int) bool {
	slot := &s.slots[i]
	if slot.state == 2 {
		return slot.valid
	}
	if slot.state == 1 || depth >= 200 {
		slot.valid = false
		s.diagnostic("unsupported_dependency", "proxy dependency cycle or depth limit exceeded")
		slot.state = 2
		return false
	}
	slot.state = 1
	if slot.dependency != "" {
		indices := s.names[slot.dependency]
		invalidDep := len(indices) == 0
		for _, j := range indices {
			if !s.resolve(j, depth+1) {
				invalidDep = true
			}
		}
		for j := 1; j < len(indices); j++ {
			if s.slots[indices[j]].key != s.slots[indices[0]].key {
				invalidDep = true
			}
		}
		if invalidDep {
			slot.valid = false
		} else {
			node, err := s.node(i)
			if err != nil {
				s.err = err
				slot.valid = false
			} else {
				slot.key, err = IdentityKey(node.Config, node.Format, node.Dependencies)
				slot.valid = err == nil
				if err != nil {
					s.err = err
				}
			}
		}
	}
	if !slot.valid {
		s.diagnostic("unsupported_dependency", fmt.Sprintf("missing or ambiguous dependency %q", slot.dependency))
	}
	slot.state = 2
	return slot.valid
}

func (s *sourceSpool) emit(ctx context.Context, emit func([]Node) error) error {
	emitted := map[string]bool{}
	var batch []Node
	included := map[string]bool{}
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := emit(batch); err != nil {
			return err
		}
		batch = nil
		included = map[string]bool{}
		return nil
	}
	var add func(int) error
	add = func(i int) error {
		slot := s.slots[i]
		if included[slot.key] {
			return nil
		}
		if slot.dependency != "" {
			if err := add(s.names[slot.dependency][0]); err != nil {
				return err
			}
		}
		node, err := s.node(i)
		if err != nil {
			return err
		}
		batch = append(batch, node)
		included[slot.key] = true
		return nil
	}
	for i, slot := range s.slots {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !slot.valid || emitted[slot.key] {
			continue
		}
		size := 0
		for j := i; !included[s.slots[j].key]; {
			size++
			if s.slots[j].dependency == "" {
				break
			}
			j = s.names[s.slots[j].dependency][0]
		}
		if len(batch)+size > 200 {
			if err := flush(); err != nil {
				return err
			}
		}
		if err := add(i); err != nil {
			return err
		}
		emitted[slot.key] = true
	}
	return flush()
}

func (s *sourceSpool) sourceFailure(path, code, message string) {
	s.summary.Partial = true
	if len(s.summary.Diagnostics) < 100 {
		s.summary.Diagnostics = append(s.summary.Diagnostics, Diagnostic{Node: path, Code: code, Message: message})
	}
}

func (s *sourceSpool) providerURLs(value any) map[string]string {
	providers := map[string]string{}
	entries, ok := value.(map[string]any)
	if !ok {
		s.sourceFailure("", "provider_failed", "providers must be a mapping")
		return providers
	}
	for name, entry := range entries {
		config, _ := entry.(map[string]any)
		address, _ := config["url"].(string)
		if strings.TrimSpace(address) == "" {
			s.sourceFailure("", "provider_failed", "provider URL required")
			continue
		}
		providers[name] = address
	}
	return providers
}

func (s *sourceSpool) rollback(start int, offset int64) error {
	for _, slot := range s.slots[start:] {
		indices := s.names[slot.name]
		for len(indices) > 0 && indices[len(indices)-1] >= start {
			indices = indices[:len(indices)-1]
		}
		if len(indices) == 0 {
			delete(s.names, slot.name)
		} else {
			s.names[slot.name] = indices
		}
	}
	s.slots = s.slots[:start]
	s.bytes = offset
	if err := s.file.Truncate(offset); err != nil {
		return fmt.Errorf("%w: %w", errSourceStorage, err)
	}
	if _, err := s.file.Seek(offset, 0); err != nil {
		return fmt.Errorf("%w: %w", errSourceStorage, err)
	}
	return nil
}

func supportedSourceKind(kind string, format Format) bool {
	if format == ClashYAML {
		switch kind {
		case "ss", "ssr", "vmess", "vless", "trojan", "hysteria", "hysteria2", "tuic", "wireguard", "socks5", "http", "snell", "mieru", "anytls", "direct", "ssh", "sudoku", "masque", "trusttunnel":
			return true
		}
	} else {
		switch kind {
		case "shadowsocks", "shadowtls", "vmess", "vless", "trojan", "hysteria", "hysteria2", "tuic", "wireguard", "socks", "http", "snell", "ssh", "anytls", "direct", "naive", "tor":
			return true
		}
	}
	return false
}

func isRemoteHTTPURL(address string) bool {
	return strings.HasPrefix(address, "http://") || strings.HasPrefix(address, "https://")
}

func isInlineOrDataSource(address string) bool {
	return strings.HasPrefix(address, "inline:") || strings.HasPrefix(address, "data:")
}

func resolveProviderAddress(parentAddress, child string) (string, error) {
	child = strings.TrimSpace(child)
	if child == "" {
		return "", errors.New("invalid provider URL")
	}
	if isRemoteHTTPURL(parentAddress) {
		parent, err := url.Parse(parentAddress)
		if err != nil || parent.Host == "" {
			return "", errors.New("invalid source URL")
		}
		relative, err := url.Parse(child)
		if err != nil {
			return "", errors.New("invalid provider URL")
		}
		resolved := parent.ResolveReference(relative)
		if (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Host == "" {
			return "", errors.New("remote provider URL must use http or https")
		}
		return resolved.String(), nil
	}
	if isInlineOrDataSource(parentAddress) {
		if !isRemoteHTTPURL(child) && !isInlineOrDataSource(child) {
			return "", errors.New("inline or data source provider cannot reference local files")
		}
		return child, nil
	}
	if isRemoteHTTPURL(child) || isInlineOrDataSource(child) {
		return child, nil
	}
	if u, err := url.Parse(child); err == nil && u.Scheme != "" && len(u.Scheme) > 1 {
		return "", errors.New("unsupported provider URL scheme")
	}
	if filepath.IsAbs(child) {
		return filepath.Clean(child), nil
	}
	return filepath.Join(filepath.Dir(parentAddress), child), nil
}

func fatalSourceError(ctx context.Context, err error) bool {
	return ctx.Err() != nil || errors.Is(err, errSourceBudget) || errors.Is(err, errSourceStorage)
}

func trimSourceContent(content []byte) []byte {
	content = bytes.TrimSpace(content)
	for bytes.HasPrefix(content, []byte{0xef, 0xbb, 0xbf}) {
		content = bytes.TrimSpace(content[3:])
	}
	return content
}

func looksLikeBase64(content []byte) bool {
	n := 0
	for _, b := range content {
		switch {
		case b == ' ' || b == '\t' || b == '\r' || b == '\n':
			continue
		case (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '+' || b == '/' || b == '=' || b == '-' || b == '_':
			n++
		default:
			return false
		}
	}
	return n >= 4
}

func stripASCIIWhitespace(content []byte) []byte {
	out := make([]byte, 0, len(content))
	for _, b := range content {
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			out = append(out, b)
		}
	}
	return out
}

func sourceURIList(content []byte) bool {
	found := false
	rest := content
	for len(rest) > 0 {
		var raw []byte
		if idx := bytes.IndexByte(rest, '\n'); idx >= 0 {
			raw, rest = rest[:idx], rest[idx+1:]
		} else {
			raw, rest = rest, nil
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			continue
		}
		if colon := bytes.IndexByte(trimmed, ':'); colon >= 0 {
			value := trimmed[colon+1:]
			if !bytes.HasPrefix(value, []byte("//")) {
				key := strings.Trim(string(bytes.TrimSpace(trimmed[:colon])), "\"'")
				switch key {
				case "proxies", "Proxy", "proxy-providers", "outbounds", "endpoints":
					return false
				}
			}
		}
		found = found || sourceURILine(string(trimmed))
	}
	return found
}

func sourceURILine(line string) bool {
	u, err := url.Parse(line)
	return err == nil && u.Scheme != "" && strings.HasPrefix(line, u.Scheme+"://")
}

func sourceURIConfig(line string) (map[string]any, bool, error) {
	u, err := url.Parse(line)
	if err != nil {
		return nil, false, err
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, false, err
	}
	metadata := false
	if strings.EqualFold(u.Scheme, "vless") || strings.EqualFold(u.Scheme, "trojan") {
		for _, key := range []string{"spx", "ech"} {
			if values, ok := q[key]; ok {
				if len(values) != 1 {
					return nil, false, errors.New("repeated legacy URI hint")
				}
				q.Del(key)
				metadata = true
			}
		}
	}
	if metadata {
		u.RawQuery = q.Encode()
		line = u.String()
	}
	config, err := DecodeURI(line)
	return config, metadata, err
}

type sourceTransport struct {
	base     http.RoundTripper
	ua       string
	result   *SourceResult
	captured bool
}

func (t *sourceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	request := req.Clone(req.Context())
	request.Header = req.Header.Clone()
	request.Header.Set("User-Agent", t.ua)
	response, err := t.base.RoundTrip(request)
	if err == nil && response.StatusCode == http.StatusOK && !t.captured {
		t.captured = true
		for _, name := range []string{"Subscription-Userinfo", "Profile-Update-Interval", "Profile-Web-Page-Url", "Content-Disposition"} {
			if v := response.Header.Get(name); len(v) <= 4096 && !strings.ContainsAny(v, "\r\n") && v != "" {
				t.result.Headers.Set(name, v)
			}
		}
	}
	return response, err
}

func buildSourceClient(client *http.Client, request SourceRequest, result *SourceResult) (*http.Client, func(), error) {
	c := http.Client{Timeout: 30 * time.Second}
	if client != nil {
		c = *client
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	closeTransport := func() {}
	if request.Proxy != "" {
		proxy, err := url.Parse(request.Proxy)
		if err != nil || proxy.Host == "" || (proxy.Scheme != "http" && proxy.Scheme != "https" && proxy.Scheme != "socks5" && proxy.Scheme != "socks5h") {
			return nil, nil, errors.New("invalid source proxy")
		}
		base, ok := transport.(*http.Transport)
		if !ok {
			return nil, nil, errors.New("source proxy requires HTTP transport")
		}
		cloned := base.Clone()
		cloned.Proxy = http.ProxyURL(proxy)
		transport = cloned
		closeTransport = cloned.CloseIdleConnections
	}
	c.Transport = &sourceTransport{base: transport, ua: ClashUserAgent(request.UserAgent), result: result}
	return &c, closeTransport, nil
}
