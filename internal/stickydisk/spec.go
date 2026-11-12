package stickydisk

import (
	"fmt"
	"strings"
)

// CacheRequest is one newline-delimited sticky_cache record after mode aliases
// and options have been validated.
type CacheRequest struct {
	Mode   CacheMode
	Paths  []string
	Custom bool
}

type parsedCacheRecord struct {
	name  string
	paths []string
}

// ParseCacheRequests parses the sticky_cache input. Newlines separate cache
// records; commas separate a mode name from its key=value options.
func ParseCacheRequests(entries []string) ([]CacheRequest, error) {
	var requests []CacheRequest
	builtinIndexes := make(map[string]int)
	customIndex := -1
	customPathSeen := make(map[string]bool)

	for lineNumber, entry := range entries {
		record, err := parseCacheRecord(entry)
		if err != nil {
			return nil, fmt.Errorf("sticky_cache line %d: %w", lineNumber+1, err)
		}

		name := strings.ToLower(record.name)
		if canonical, ok := modeAliases[name]; ok {
			name = canonical
		}

		if name == "custom" {
			if len(record.paths) == 0 {
				return nil, fmt.Errorf("sticky_cache line %d: custom cache requires at least one path option", lineNumber+1)
			}
			if customIndex == -1 {
				requests = append(requests, CacheRequest{Custom: true})
				customIndex = len(requests) - 1
			}
			request := &requests[customIndex]
			for _, path := range record.paths {
				if !customPathSeen[path] {
					customPathSeen[path] = true
					request.Paths = append(request.Paths, path)
				}
			}
			continue
		}

		mode, ok := cacheModes[name]
		if !ok {
			return nil, fmt.Errorf("sticky_cache line %d: unknown cache mode %q (valid modes: %s, custom)", lineNumber+1, record.name, strings.Join(ValidModes(), ", "))
		}
		if len(record.paths) > 0 {
			return nil, fmt.Errorf("sticky_cache line %d: option %q is only supported by the custom cache mode", lineNumber+1, "path")
		}

		if _, exists := builtinIndexes[name]; exists {
			continue
		}

		builtinIndexes[name] = len(requests)
		requests = append(requests, CacheRequest{Mode: mode})
	}

	return requests, nil
}

func parseCacheRecord(entry string) (parsedCacheRecord, error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return parsedCacheRecord{}, fmt.Errorf("cache record is empty")
	}

	fields := strings.Split(entry, ",")
	name := strings.TrimSpace(fields[0])
	if name == "" {
		return parsedCacheRecord{}, fmt.Errorf("cache mode is empty")
	}
	if strings.Contains(name, "/") {
		return parsedCacheRecord{}, fmt.Errorf("slash-delimited cache options are not supported; use mode,key=value")
	}

	record := parsedCacheRecord{name: name}
	for _, field := range fields[1:] {
		field = strings.TrimSpace(field)
		if field == "" {
			return parsedCacheRecord{}, fmt.Errorf("cache option is empty")
		}
		key, value, found := strings.Cut(field, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if !found || key == "" || value == "" {
			return parsedCacheRecord{}, fmt.Errorf("cache option %q must use key=value with a non-empty value", field)
		}

		switch key {
		case "path":
			record.paths = append(record.paths, value)
		default:
			return parsedCacheRecord{}, fmt.Errorf("unknown cache option %q", key)
		}
	}

	return record, nil
}
