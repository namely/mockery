package test

import (
	"github.com/namely/mockery/v2/pkg/fixtures/http"
)

type HasConflictingNestedImports interface {
	RequesterNS
	Z() http.MyStruct
}
