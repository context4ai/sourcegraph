package contract

// ReadItemRequest selects an inclusive range within one repository snapshot.
type ReadItemRequest struct {
	Path      string
	StartLine int
	EndLine   int
}
