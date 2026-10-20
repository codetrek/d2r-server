package guestlayout

import "fmt"

type Module string

const (
	ModuleMain Module = "main"
	ModuleSDK  Module = "sdk"
)

const splitSectionsProfile = "dm-split-sections-v1"

type layoutProfile struct {
	module       Module
	sectionNames [3][2]string
	imageName    string
}

func profileFor(module Module) (layoutProfile, error) {
	if module != ModuleMain && module != ModuleSDK {
		return layoutProfile{}, fmt.Errorf("module must be explicitly set to main or sdk, got %q", module)
	}
	return layoutProfile{
		module: module,
		sectionNames: [3][2]string{
			{"__TEXT", "__dm" + string(module) + "x0"},
			{"__DM" + string(module) + "W", "__img"},
			{"__TEXT", "__dm" + string(module) + "x1"},
		},
		imageName: string(module) + ".patched.reconstructed.bin",
	}, nil
}
