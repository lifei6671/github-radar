// Package docs 提供与设计文档共用的正式内置词表。
package docs

import _ "embed"

//go:embed technical-taxonomy-v1.yaml
var TechnicalTaxonomy []byte
