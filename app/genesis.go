package app

import "encoding/json"

type GenesisState map[string]json.RawMessage

func NewDefaultGenesisState() GenesisState {
	encodingConfig := MakeEncodingConfig()
	return ModuleBasics.DefaultGenesis(encodingConfig.Marshaler)
}