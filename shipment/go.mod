module github.com/adamkekesi/microservice-demo/shipment

go 1.25.0

require github.com/adamkekesi/microservice-demo/platform v0.0.0

// The shared platform module is unpublished; resolve it from the local
// filesystem. go.work also handles this for local dev, but the explicit
// replace keeps `go mod tidy`, CI, and the Docker build working without it.
replace github.com/adamkekesi/microservice-demo/platform => ../platform
