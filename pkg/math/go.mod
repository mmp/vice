module github.com/mmp/vice/pkg/math

go 1.22.4

require golang.org/x/exp v0.0.0-20240613232115-7f521ea00fb8

require (
	github.com/MichaelTJones/pcg v0.0.0-20180122055547-df440c6ed7ed // indirect
	github.com/mmp/vice/pkg/rand v0.0.0-00010101000000-000000000000 // indirect
)

replace github.com/mmp/vice/pkg/rand => ../rand
