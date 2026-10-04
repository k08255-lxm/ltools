package screenshot2

// SaveDialogOptions contains options for the save file dialog
type SaveDialogOptions struct {
	DefaultFilename string   // Default filename to show
	DefaultDir      string   // Default directory
	AllowedTypes    []string // Allowed file extensions (e.g., "png", "jpg")
	Title           string   // Dialog title
	ParentWindow    any      // Parent window reference (interface{} to avoid import cycles)
}
