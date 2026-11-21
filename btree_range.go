package btree

// EndpointInclusion specifies whether a range endpoint is part of the range.
type EndpointInclusion = bool

const (
	// Open excludes the endpoint from a range.
	Open EndpointInclusion = false
	// Closed includes the endpoint in a range.
	Closed EndpointInclusion = true

	// Exclusive and Inclusive are aliases for Open and Closed.
	Exclusive EndpointInclusion = Open
	Inclusive EndpointInclusion = Closed
)
