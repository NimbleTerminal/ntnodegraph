// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import (
	"cmp"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

const (
	// MaxIDLen bounds node IDs, port names, group IDs, and graph names.
	MaxIDLen = 128
	// MaxCapabilityLen bounds a single capability declaration string.
	MaxCapabilityLen = 512
)

// Standard node type namespaces. Other runtimes may register additional valid
// lowercase namespaces; NamespaceGraph is reserved for flatten-at-load
// subgraphs.
const (
	NamespaceSource    = "source"
	NamespaceTransform = "transform"
	NamespaceSurface   = "surface"
	NamespaceWASM      = "wasm"
	NamespaceGPU       = "gpu"
	NamespaceGraph     = "graph"
	NamespaceAgent     = "agent"
)

// Sentinel errors returned by identifier, type, and compatibility checks.
var (
	ErrInvalidID         = errors.New("ntnodegraph: invalid identifier")
	ErrInvalidTypeID     = errors.New("ntnodegraph: invalid node type id")
	ErrInvalidPortRef    = errors.New("ntnodegraph: invalid port reference")
	ErrInvalidDataType   = errors.New("ntnodegraph: invalid data type")
	ErrInvalidCapability = errors.New("ntnodegraph: invalid capability")
	ErrTypeMismatch      = errors.New("ntnodegraph: incompatible port types")
)

// IsValidID reports whether s is a valid identifier for a graph name, node ID,
// port name, parameter name, or group ID ([A-Za-z0-9][A-Za-z0-9_-]*).
func IsValidID(s string) bool {
	if s == "" || len(s) > MaxIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if i == 0 {
			if !isAlphaNum {
				return false
			}
			continue
		}
		if !isAlphaNum && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func isValidNamespace(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 0 {
			if c < 'a' || c > 'z' {
				return false
			}
			continue
		}
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func isValidTypeName(s string) bool {
	if s == "" || len(s) > MaxIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isLowerNum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if i == 0 || i == len(s)-1 {
			if !isLowerNum {
				return false
			}
			continue
		}
		if !isLowerNum && c != '.' && c != '_' && c != '-' {
			return false
		}
	}
	return !strings.Contains(s, "..")
}

// TypeID identifies a versioned node type in namespace/name@major form,
// such as "source/http@1" or "wasm/neomantra.hn-typesetter@1".
type TypeID struct {
	Namespace string
	Name      string
	Major     int
}

// ParseTypeID parses a canonical "namespace/name@major" node type identifier.
func ParseTypeID(s string) (TypeID, error) {
	slash := strings.IndexByte(s, '/')
	at := strings.LastIndexByte(s, '@')
	if slash <= 0 || at <= slash+1 || at == len(s)-1 {
		return TypeID{}, fmt.Errorf("%w %q: expected namespace/name@major", ErrInvalidTypeID, s)
	}
	ns := s[:slash]
	name := s[slash+1 : at]
	verStr := s[at+1:]
	if strings.ContainsRune(name, '/') || strings.ContainsRune(name, '@') {
		return TypeID{}, fmt.Errorf("%w %q: unexpected separator in name", ErrInvalidTypeID, s)
	}
	if !isValidNamespace(ns) {
		return TypeID{}, fmt.Errorf("%w %q: invalid namespace %q", ErrInvalidTypeID, s, ns)
	}
	if !isValidTypeName(name) {
		return TypeID{}, fmt.Errorf("%w %q: invalid type name %q", ErrInvalidTypeID, s, name)
	}
	if verStr[0] < '1' || verStr[0] > '9' {
		return TypeID{}, fmt.Errorf("%w %q: major version must be a positive integer without leading zeros", ErrInvalidTypeID, s)
	}
	for i := 1; i < len(verStr); i++ {
		if verStr[i] < '0' || verStr[i] > '9' {
			return TypeID{}, fmt.Errorf("%w %q: invalid major version %q", ErrInvalidTypeID, s, verStr)
		}
	}
	major, err := strconv.Atoi(verStr)
	if err != nil || major < 1 {
		return TypeID{}, fmt.Errorf("%w %q: invalid major version %q", ErrInvalidTypeID, s, verStr)
	}
	return TypeID{Namespace: ns, Name: name, Major: major}, nil
}

// MustParseTypeID parses s and panics if invalid. Intended for static test and
// registration constants.
func MustParseTypeID(s string) TypeID {
	id, err := ParseTypeID(s)
	if err != nil {
		panic(err)
	}
	return id
}

// IsZero reports whether t is the zero TypeID.
func (t TypeID) IsZero() bool {
	return t.Namespace == "" && t.Name == "" && t.Major == 0
}

// Valid reports whether t satisfies TypeID grammar rules.
func (t TypeID) Valid() bool {
	return isValidNamespace(t.Namespace) && isValidTypeName(t.Name) && t.Major >= 1
}

// String formats t as "namespace/name@major".
func (t TypeID) String() string {
	if t.IsZero() {
		return ""
	}
	return t.Namespace + "/" + t.Name + "@" + strconv.Itoa(t.Major)
}

// CompareTypeID orders TypeIDs deterministically by namespace, name, then major version.
func CompareTypeID(a, b TypeID) int {
	if c := cmp.Compare(a.Namespace, b.Namespace); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Name, b.Name); c != 0 {
		return c
	}
	return cmp.Compare(a.Major, b.Major)
}

// MarshalText implements encoding.TextMarshaler.
func (t TypeID) MarshalText() ([]byte, error) {
	if !t.Valid() {
		return nil, fmt.Errorf("%w: %+v", ErrInvalidTypeID, t)
	}
	return []byte(t.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (t *TypeID) UnmarshalText(text []byte) error {
	parsed, err := ParseTypeID(string(text))
	if err != nil {
		return err
	}
	*t = parsed
	return nil
}

// PortRef addresses a named port on a node as "node.port".
type PortRef struct {
	Node string
	Port string
}

// ParsePortRef parses a "node.port" reference.
func ParsePortRef(s string) (PortRef, error) {
	dot := strings.IndexByte(s, '.')
	if dot <= 0 || dot == len(s)-1 || strings.IndexByte(s[dot+1:], '.') >= 0 {
		return PortRef{}, fmt.Errorf("%w %q: expected node.port", ErrInvalidPortRef, s)
	}
	nodeID := s[:dot]
	portName := s[dot+1:]
	if !IsValidID(nodeID) {
		return PortRef{}, fmt.Errorf("%w %q: invalid node id %q", ErrInvalidPortRef, s, nodeID)
	}
	if !IsValidID(portName) {
		return PortRef{}, fmt.Errorf("%w %q: invalid port name %q", ErrInvalidPortRef, s, portName)
	}
	return PortRef{Node: nodeID, Port: portName}, nil
}

// MustParsePortRef parses s and panics if invalid.
func MustParsePortRef(s string) PortRef {
	ref, err := ParsePortRef(s)
	if err != nil {
		panic(err)
	}
	return ref
}

// IsZero reports whether p is empty.
func (p PortRef) IsZero() bool {
	return p.Node == "" && p.Port == ""
}

// Valid reports whether both Node and Port are valid identifiers.
func (p PortRef) Valid() bool {
	return IsValidID(p.Node) && IsValidID(p.Port)
}

// String formats p as "node.port".
func (p PortRef) String() string {
	if p.IsZero() {
		return ""
	}
	return p.Node + "." + p.Port
}

// ComparePortRef orders PortRefs deterministically by Node then Port.
func ComparePortRef(a, b PortRef) int {
	if c := cmp.Compare(a.Node, b.Node); c != 0 {
		return c
	}
	return cmp.Compare(a.Port, b.Port)
}

// MarshalText implements encoding.TextMarshaler.
func (p PortRef) MarshalText() ([]byte, error) {
	if !p.Valid() {
		return nil, fmt.Errorf("%w: %+v", ErrInvalidPortRef, p)
	}
	return []byte(p.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (p *PortRef) UnmarshalText(text []byte) error {
	parsed, err := ParsePortRef(string(text))
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// BaseType is the root kind in the ntnodegraph port type system.
type BaseType string

// Initial ntnodegraph base types.
const (
	BaseBytes    BaseType = "bytes"
	BaseString   BaseType = "string"
	BaseNumber   BaseType = "number"
	BaseBool     BaseType = "bool"
	BaseJSON     BaseType = "json"
	BaseTable    BaseType = "table"
	BaseDocument BaseType = "document"
	BaseFrame    BaseType = "frame"
	BaseTexture  BaseType = "texture"
	BaseScene    BaseType = "scene"
	BaseEvent    BaseType = "event"
	BaseAny      BaseType = "any"
)

// Valid reports whether b is a standard base type or a namespaced opaque type.
func (b BaseType) Valid() bool {
	switch b {
	case BaseBytes, BaseString, BaseNumber, BaseBool, BaseJSON,
		BaseTable, BaseDocument, BaseFrame, BaseTexture, BaseScene, BaseEvent, BaseAny:
		return true
	default:
		ns, name, ok := strings.Cut(string(b), "/")
		return ok && isValidNamespace(ns) && isValidTypeName(name)
	}
}

// AllowsTypeParam reports whether b may carry a <param> specialization such as
// frame<rgba8>, texture<rgba16f>, scene<sdf>, table<arrow>, or event<string>.
func (b BaseType) AllowsTypeParam() bool {
	switch b {
	case BaseTable, BaseDocument, BaseFrame, BaseTexture, BaseScene, BaseEvent:
		return true
	default:
		return false
	}
}

func isValidTypeParam(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isLowerNum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if i == 0 || i == len(s)-1 {
			if !isLowerNum {
				return false
			}
			continue
		}
		if !isLowerNum && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

// DataType represents a port's static type, optionally parameterized (for
// example "frame<rgba8>").
type DataType struct {
	Base  BaseType
	Param string
}

// Common unparameterized DataType values.
var (
	TypeBytes    = DataType{Base: BaseBytes}
	TypeString   = DataType{Base: BaseString}
	TypeNumber   = DataType{Base: BaseNumber}
	TypeBool     = DataType{Base: BaseBool}
	TypeJSON     = DataType{Base: BaseJSON}
	TypeTable    = DataType{Base: BaseTable}
	TypeDocument = DataType{Base: BaseDocument}
	TypeFrame    = DataType{Base: BaseFrame}
	TypeTexture  = DataType{Base: BaseTexture}
	TypeScene    = DataType{Base: BaseScene}
	TypeEvent    = DataType{Base: BaseEvent}
	TypeAny      = DataType{Base: BaseAny}
)

// ParseDataType parses a port DataType string such as "document" or "frame<rgba8>".
func ParseDataType(s string) (DataType, error) {
	if s == "" {
		return DataType{}, fmt.Errorf("%w: empty type", ErrInvalidDataType)
	}
	lt := strings.IndexByte(s, '<')
	if lt < 0 {
		if strings.IndexByte(s, '>') >= 0 {
			return DataType{}, fmt.Errorf("%w %q: unmatched '>'", ErrInvalidDataType, s)
		}
		base := BaseType(s)
		if !base.Valid() {
			return DataType{}, fmt.Errorf("%w %q: unknown base type", ErrInvalidDataType, s)
		}
		return DataType{Base: base}, nil
	}
	if !strings.HasSuffix(s, ">") || strings.IndexByte(s[lt+1:], '<') >= 0 || strings.IndexByte(s[:len(s)-1], '>') >= 0 {
		return DataType{}, fmt.Errorf("%w %q: malformed type parameter", ErrInvalidDataType, s)
	}
	base := BaseType(s[:lt])
	param := s[lt+1 : len(s)-1]
	if !base.Valid() {
		return DataType{}, fmt.Errorf("%w %q: unknown base type %q", ErrInvalidDataType, s, base)
	}
	if !base.AllowsTypeParam() {
		return DataType{}, fmt.Errorf("%w %q: base type %q does not accept a type parameter", ErrInvalidDataType, s, base)
	}
	if !isValidTypeParam(param) {
		return DataType{}, fmt.Errorf("%w %q: invalid type parameter %q", ErrInvalidDataType, s, param)
	}
	return DataType{Base: base, Param: param}, nil
}

// MustParseDataType parses s and panics if invalid.
func MustParseDataType(s string) DataType {
	dt, err := ParseDataType(s)
	if err != nil {
		panic(err)
	}
	return dt
}

// Valid reports whether d is a well-formed DataType.
func (d DataType) Valid() bool {
	if !d.Base.Valid() {
		return false
	}
	if d.Param == "" {
		return true
	}
	return d.Base.AllowsTypeParam() && isValidTypeParam(d.Param)
}

// String formats d as "base" or "base<param>".
func (d DataType) String() string {
	if d.Param == "" {
		return string(d.Base)
	}
	return string(d.Base) + "<" + d.Param + ">"
}

// MarshalText implements encoding.TextMarshaler.
func (d DataType) MarshalText() ([]byte, error) {
	if !d.Valid() {
		return nil, fmt.Errorf("%w: %+v", ErrInvalidDataType, d)
	}
	return []byte(d.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *DataType) UnmarshalText(text []byte) error {
	parsed, err := ParseDataType(string(text))
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// Compatibility classifies how an outlet DataType connects to an inlet DataType.
type Compatibility uint8

const (
	// CompatIncompatible indicates the connection is rejected.
	CompatIncompatible Compatibility = iota
	// CompatExact indicates identical DataTypes.
	CompatExact
	// CompatWildcard indicates subtyping without data conversion (T -> any, or B<p> -> B).
	CompatWildcard
	// CompatCoerce indicates an explicit, conservative conversion at the edge boundary.
	CompatCoerce
)

// CoercionKind names the conservative edge boundary conversion selected by
// CheckCompatibility when Compatibility == CompatCoerce.
type CoercionKind string

const (
	CoerceNone            CoercionKind = ""
	CoerceUTF8Bytes       CoercionKind = "string->bytes"
	CoerceBytesUTF8       CoercionKind = "bytes->string"
	CoerceJSONBytes       CoercionKind = "json->bytes"
	CoerceJSONString      CoercionKind = "json->string"
	CoerceJSONEncode      CoercionKind = "scalar->json"
	CoerceJSONParse       CoercionKind = "bytes->json"
	CoerceFrameUpload     CoercionKind = "frame->texture"
	CoerceTextureDownload CoercionKind = "texture->frame"
	CoerceEventTrigger    CoercionKind = "value->event"
)

// CheckCompatibility determines whether an outlet of type out may wire to an
// inlet of type in under ntnodegraph's conservative type and coercion rules:
//
//  1. Exact match (T -> T) is CompatExact.
//  2. Any concrete type into "any" (T -> any) is CompatWildcard; "any" into a
//     concrete type (any -> T) is rejected.
//  3. Parameterized specialization into unparameterized base (B<p> -> B, such as
//     frame<rgba8> -> frame) is CompatWildcard; the reverse (B -> B<p>) and
//     mismatched parameters (B<p> -> B<q>) are rejected.
//  4. Any non-"any" value into unparameterized "event" triggers a bang
//     (CoerceEventTrigger); "event" never coerces into non-event types.
//  5. Conservative UTF-8 / JSON boundary conversions:
//     string <-> bytes, json -> bytes, json -> string,
//     number|bool|string -> json, and bytes -> json (syntax-checked at delivery,
//     enabling e.g. source/http.body -> typeset.json).
//     Cross-scalar coercions (number <-> bool, string <-> number, string <-> bool)
//     are strictly forbidden.
//  6. GPU/CPU image boundary conversions (texture -> frame download and
//     frame -> texture upload) are allowed when the inlet is unparameterized or
//     both carry identical pixel-format parameters (e.g. texture<rgba8> -> frame<rgba8>).
func CheckCompatibility(out, in DataType) (Compatibility, CoercionKind, error) {
	if !out.Valid() {
		return CompatIncompatible, CoerceNone, fmt.Errorf("%w: invalid outlet type %q", ErrInvalidDataType, out)
	}
	if !in.Valid() {
		return CompatIncompatible, CoerceNone, fmt.Errorf("%w: invalid inlet type %q", ErrInvalidDataType, in)
	}
	if out == in {
		return CompatExact, CoerceNone, nil
	}
	if in.Base == BaseAny {
		return CompatWildcard, CoerceNone, nil
	}
	if out.Base == BaseAny {
		return CompatIncompatible, CoerceNone, fmt.Errorf("%w: untyped outlet %q cannot feed typed inlet %q", ErrTypeMismatch, out, in)
	}
	if out.Base == in.Base && out.Param != "" && in.Param == "" {
		return CompatWildcard, CoerceNone, nil
	}
	if strings.ContainsRune(string(out.Base), '/') || strings.ContainsRune(string(in.Base), '/') {
		return ExactCompatibility(out, in)
	}
	if in.Base == BaseEvent && in.Param == "" {
		return CompatCoerce, CoerceEventTrigger, nil
	}

	if out.Param == "" && in.Param == "" {
		switch {
		case out.Base == BaseString && in.Base == BaseBytes:
			return CompatCoerce, CoerceUTF8Bytes, nil
		case out.Base == BaseBytes && in.Base == BaseString:
			return CompatCoerce, CoerceBytesUTF8, nil
		case out.Base == BaseJSON && in.Base == BaseBytes:
			return CompatCoerce, CoerceJSONBytes, nil
		case out.Base == BaseJSON && in.Base == BaseString:
			return CompatCoerce, CoerceJSONString, nil
		case (out.Base == BaseNumber || out.Base == BaseBool || out.Base == BaseString) && in.Base == BaseJSON:
			return CompatCoerce, CoerceJSONEncode, nil
		case out.Base == BaseBytes && in.Base == BaseJSON:
			return CompatCoerce, CoerceJSONParse, nil
		}
	}

	pixelFormatMatch := in.Param == "" || (out.Param != "" && out.Param == in.Param)
	if pixelFormatMatch {
		if out.Base == BaseTexture && in.Base == BaseFrame {
			return CompatCoerce, CoerceTextureDownload, nil
		}
		if out.Base == BaseFrame && in.Base == BaseTexture {
			return CompatCoerce, CoerceFrameUpload, nil
		}
	}

	return CompatIncompatible, CoerceNone, fmt.Errorf("%w: outlet %q cannot connect to inlet %q", ErrTypeMismatch, out, in)
}

// Capability represents a host permission declared on a node in "<kind>" or
// "<kind>:<scope>" form, such as "net:hacker-news.firebaseio.com".
type Capability struct {
	Kind  string
	Scope string
}

// ParseCapability parses and validates a capability declaration string.
func ParseCapability(s string) (Capability, error) {
	if s == "" || len(s) > MaxCapabilityLen {
		return Capability{}, fmt.Errorf("%w %q: empty or exceeds %d bytes", ErrInvalidCapability, s, MaxCapabilityLen)
	}
	for _, r := range s {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return Capability{}, fmt.Errorf("%w %q: must not contain whitespace or control characters", ErrInvalidCapability, s)
		}
	}
	colon := strings.IndexByte(s, ':')
	if colon < 0 {
		if !isValidNamespace(s) {
			return Capability{}, fmt.Errorf("%w %q: invalid capability kind", ErrInvalidCapability, s)
		}
		return Capability{Kind: s}, nil
	}
	kind := s[:colon]
	scope := s[colon+1:]
	if !isValidNamespace(kind) {
		return Capability{}, fmt.Errorf("%w %q: invalid capability kind %q", ErrInvalidCapability, s, kind)
	}
	if scope == "" {
		return Capability{}, fmt.Errorf("%w %q: empty capability scope after ':'", ErrInvalidCapability, s)
	}
	return Capability{Kind: kind, Scope: scope}, nil
}

// MustParseCapability parses s and panics if invalid.
func MustParseCapability(s string) Capability {
	c, err := ParseCapability(s)
	if err != nil {
		panic(err)
	}
	return c
}

// Valid reports whether c has a valid Kind and well-formed optional Scope.
func (c Capability) Valid() bool {
	_, err := ParseCapability(c.String())
	return err == nil
}

// String formats c as "kind" or "kind:scope".
func (c Capability) String() string {
	if c.Scope == "" {
		return c.Kind
	}
	return c.Kind + ":" + c.Scope
}

// Covers reports whether a granted/declared capability c satisfies req.
//
// Matching is fail-closed and conservative:
//   - Kinds must match exactly.
//   - If req has no scope, any capability of the same Kind covers it.
//   - Scope "*" covers any valid scope of the same Kind.
//   - For Kind == "net", host comparisons are ASCII case-insensitive, and a
//     leading "*." wildcard (e.g. "net:*.firebaseio.com") covers strict
//     subdomains ("hacker-news.firebaseio.com") without covering the bare parent
//     ("firebaseio.com").
//   - For Kind == "fs", slash-separated scopes are normalized with path.Clean,
//     any scope retaining a ".." segment is rejected, and prefix scopes ending
//     in "/*" cover only paths strictly beneath that cleaned directory.
func (c Capability) Covers(req Capability) bool {
	if c.Kind == "" || c.Kind != req.Kind {
		return false
	}
	if req.Scope == "" {
		if c.Kind == "fs" && c.Scope != "" && c.Scope != "*" {
			_, ok := cleanFSScopeBase(c.Scope)
			return ok
		}
		return true
	}
	if c.Scope == "" {
		return false
	}
	if c.Kind == "fs" {
		return fsCovers(c.Scope, req.Scope)
	}
	if c.Scope == "*" || c.Scope == req.Scope {
		return true
	}
	if c.Kind == "net" {
		grantedHost := strings.ToLower(c.Scope)
		reqHost := strings.ToLower(req.Scope)
		if grantedHost == reqHost {
			return true
		}
		if strings.HasPrefix(grantedHost, "*.") {
			suffix := grantedHost[1:] // ".example.com"
			return len(reqHost) > len(suffix) && strings.HasSuffix(reqHost, suffix)
		}
		return false
	}
	return false
}

func cleanFSPath(raw string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, '\\') {
		return "", false
	}
	cleaned := path.Clean(raw)
	if slices.Contains(strings.Split(cleaned, "/"), "..") {
		return "", false
	}
	return cleaned, true
}

func cleanFSScopeBase(scope string) (string, bool) {
	if strings.HasSuffix(scope, "/*") {
		rawDir := strings.TrimSuffix(scope, "/*")
		if rawDir == "" {
			return "/", true
		}
		return cleanFSPath(rawDir)
	}
	return cleanFSPath(scope)
}

func isStrictSubpath(dir, target string) bool {
	if dir == "." {
		return target != "." && !strings.HasPrefix(target, "/")
	}
	if dir == "/" {
		return strings.HasPrefix(target, "/") && target != "/"
	}
	return strings.HasPrefix(target, dir+"/") && len(target) > len(dir)+1
}

func fsCovers(grantScope, reqScope string) bool {
	if grantScope == "*" {
		if reqScope == "*" {
			return true
		}
		if strings.HasSuffix(reqScope, "/*") {
			base, ok := cleanFSScopeBase(reqScope)
			return ok && base != ""
		}
		_, ok := cleanFSPath(reqScope)
		return ok
	}
	if reqScope == "*" {
		return false
	}
	if strings.HasSuffix(grantScope, "/*") {
		grantDir, ok := cleanFSScopeBase(grantScope)
		if !ok || grantDir == "" {
			return false
		}
		if strings.HasSuffix(reqScope, "/*") {
			reqDir, reqOK := cleanFSScopeBase(reqScope)
			if !reqOK || reqDir == "" {
				return false
			}
			return reqDir == grantDir || isStrictSubpath(grantDir, reqDir)
		}
		cleanReq, reqOK := cleanFSPath(reqScope)
		if !reqOK {
			return false
		}
		return isStrictSubpath(grantDir, cleanReq)
	}
	if strings.HasSuffix(reqScope, "/*") {
		return false
	}
	cleanGrant, gOK := cleanFSPath(grantScope)
	cleanReq, rOK := cleanFSPath(reqScope)
	return gOK && rOK && cleanGrant == cleanReq
}

// CompareCapability orders capabilities deterministically by Kind then Scope.
func CompareCapability(a, b Capability) int {
	if k := cmp.Compare(a.Kind, b.Kind); k != 0 {
		return k
	}
	return cmp.Compare(a.Scope, b.Scope)
}

// MarshalText implements encoding.TextMarshaler.
func (c Capability) MarshalText() ([]byte, error) {
	if !c.Valid() {
		return nil, fmt.Errorf("%w: %+v", ErrInvalidCapability, c)
	}
	return []byte(c.String()), nil
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (c *Capability) UnmarshalText(text []byte) error {
	parsed, err := ParseCapability(string(text))
	if err != nil {
		return err
	}
	*c = parsed
	return nil
}
