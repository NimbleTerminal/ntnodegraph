// Copyright (c) 2026 Neomantra Corp.
// SPDX-License-Identifier: MIT

package ntnodegraph

import _ "embed"

// SchemaV1URI is the canonical JSON Schema $id for ntnodegraph v1 specs.
const SchemaV1URI = "https://nimbleterminal.dev/schemas/ntnodegraph/v1.schema.json"

//go:embed schema/v1.schema.json
var schemaV1Bytes []byte

// SchemaV1JSON returns a fresh copy of the canonical JSON Schema (Draft 2020-12)
// for ntnodegraph version 1 specifications.
func SchemaV1JSON() []byte {
	return append([]byte(nil), schemaV1Bytes...)
}
