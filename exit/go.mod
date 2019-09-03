module github.com/nknorg/tuna/exit

go 1.12

require (
	github.com/nknorg/nkn v1.0.2-beta
	github.com/nknorg/nkn-sdk-go v0.0.0-20190903001628-f18dce2fd449
	github.com/nknorg/tuna v0.0.0-00010101000000-000000000000
	github.com/patrickmn/go-cache v2.1.0+incompatible
	github.com/pkg/errors v0.8.1
	github.com/rdegges/go-ipify v0.0.0-20150526035502-2d94a6a86c40
	github.com/trueinsider/smux v1.0.9-0.20190902023410-0a6805b18476
)

replace github.com/nknorg/nkn => github.com/trueinsider/nkn v0.5.3-alpha.0.20190828053242-af586b32fef3

replace github.com/nknorg/tuna => ../
