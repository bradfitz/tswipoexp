package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
)

// guiStateFile holds GUI state that isn't per profile, in the state
// root so it travels with the stick. Fyne's own Preferences would
// land in the user's roaming app data on the host, which is what
// portable is trying to avoid.
const guiStateFile = "gui.json"

type guiState struct {
	WindowWidth  float32 `json:",omitempty"`
	WindowHeight float32 `json:",omitempty"`
}

func loadGUIState(root string) guiState {
	var s guiState
	b, err := os.ReadFile(filepath.Join(root, guiStateFile))
	if err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

func saveGUIState(root string, s guiState) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(root, guiStateFile), append(b, '\n'))
}

// defaultWindowSize is the initial window size when none is saved.
// It's shrunk after the window is shown if it doesn't fit the screen;
// see UI.fitToScreen.
var defaultWindowSize = fyne.NewSize(760, 660)
