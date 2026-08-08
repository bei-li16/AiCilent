//go:build gui

package main

import _ "embed"

// defaultConfigYAML is embedded so the released GUI executable can bootstrap
// itself without shipping a separate config directory.
//
//go:embed default_providers.yaml
var defaultConfigYAML []byte
