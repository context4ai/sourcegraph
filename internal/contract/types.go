package contract

type ResolveRequest struct{ Revision string }
type ReadRequest struct {
	Revision  string
	Path      string
	StartLine int
	EndLine   int
}
type ListRequest struct {
	Revision string
	Path     string
	First    int
	After    string
}

// SearchRequest mirrors rg: Pattern is an RE2 regexp matched per line against
// file contents; FixedStrings (-F), IgnoreCase (-i), Glob (-g) and Output files
// (-l) keep rg's meaning. Paths are rg's PATH arguments.
type SearchRequest struct {
	Revision     string
	Pattern      string
	FixedStrings bool
	IgnoreCase   bool
	Glob         []string
	Paths        []string
	Output       string
}
type DiffRequest struct {
	Paths []string
	Base  string
	Head  string
	First int
	After string
}
type Meta struct {
	Protocol  string
	RequestID string
	Status    string
	Code      string `json:",omitempty"`
	Retryable bool   `json:",omitempty"`
}
type ErrorResponse struct {
	Error string
	Meta  Meta
}
