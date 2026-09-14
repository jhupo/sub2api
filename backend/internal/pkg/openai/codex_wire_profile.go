package openai

import "strings"

type CodexClientInfo struct {
	Name    string
	Version string
}

// CodexWireProfile keeps the full UA together with its independent Core and
// clientInfo versions. Parsing never substitutes either version.
type CodexWireProfile struct {
	UserAgent   string
	Originator  string
	CoreVersion string
	ClientInfo  *CodexClientInfo
}

func ParseCodexWireProfile(userAgent string) (CodexWireProfile, bool) {
	originator, ua, ok := PairCodexClientIdentity(userAgent)
	if !ok {
		return CodexWireProfile{}, false
	}
	_, rest, _ := strings.Cut(ua, "/")
	core, runtime, _ := strings.Cut(rest, " ")
	if core == "" {
		return CodexWireProfile{}, false
	}
	profile := CodexWireProfile{UserAgent: ua, Originator: originator, CoreVersion: core}
	// The OS group is not clientInfo. A subsequent group, or a group with a
	// known client name, must be a complete final (name; version) declaration.
	open := strings.LastIndexByte(runtime, '(')
	if open < 0 {
		return profile, true
	}
	inner := runtime[open+1:]
	name, version, hasVersion := strings.Cut(inner, ";")
	name = strings.TrimSpace(strings.TrimSuffix(name, ")"))
	if open == 0 && !IsCodexOfficialClientOriginator(name) {
		return profile, true
	}
	if !hasVersion || !strings.HasSuffix(version, ")") || !isSaneCodexOriginator(name) {
		return CodexWireProfile{}, false
	}
	version = strings.TrimSpace(strings.TrimSuffix(version, ")"))
	if version == "" || strings.ContainsAny(version, "()") {
		return CodexWireProfile{}, false
	}
	profile.ClientInfo = &CodexClientInfo{Name: name, Version: version}
	return profile, true
}
