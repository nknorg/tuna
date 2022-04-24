# TUNA 🐟

TUNA (Tunnel Using NKN for any Application) allows anyone to tunnel any network
based services through NKN. Node will receive payment for tunneled traffic
directly from user.

Note: UDP mode is still work in progress. Please use TCP-only services for now.

## Build

Simply run `make` will do the work. The output binary name will be `tuna`.

## Get Started

Either entry mode or exit mode will need a service definition file
`services.json`. You can start by using `services.json.example` as template. The
service file defines what services to use or provide, which ports a service
uses, and various configurations like encryption.

### Entry Mode

You will need a config file `config.entry.json`. You can start by using
`config.entry.json.example` as template.

Start tuna in entry mode:

```
./tuna entry
```

Then you can start using configured services as if they're on your local machine
(e.g. `127.0.0.1:30080` for HTTP proxy).

### Exit Mode

You will need a config file `config.exit.json`. You can start by using
`config.exit.json.example` as template.

Start tuna in exit mode:

```
./tuna exit
```

Then users can connect to your services through their tuna entry and pay you NKN
based on bandwidth comsumption.

### Reverse Entry Mode

Set `reverse` to `true` in `config.entry.json` and start tuna in entry mode.
Then users can use your node as reverse proxy and pay you NKN based on bandwidth
comsumption.

### Reverse Exit Mode

Set `reverse` to `true` in `config.exit.json` and start tuna in exit mode. Then
your service will be exposed to public network by reverse entry. Your node does
not need to have public IP address or open port at all.

### Config

Entry mode config `config.entry.json`:

* `services` services you want to use
* `dialTimeout` timeout for NKN node connection
* `udpTimeout` timeout for UDP connections
* `nanoPayFee` fee used for nano pay transaction
* `reverse` should be used to provide reverse tunnel for those who don't have public IP
* `reverseBeneficiaryAddr` Beneficiary address (NKN wallet address to receive rewards)
* `reverseTCP` TCP port to listen for connections
* `reverseUDP` UDP port to listen for connections
* `reversePrice` price for reverse connections
* `reverseClaimInterval` payment claim interval for reverse connections
* `reverseSubscriptionDuration` duration for subscription in blocks
* `reverseSubscriptionFee` fee used for subscription

Exit mode config `config.exit.json`:

* `beneficiaryAddr` beneficiary address (NKN wallet address to receive rewards)
* `listenTCP` TCP port to listen for connections
* `listenUDP` UDP port to listen for connections
* `dialTimeout` timeout for connections to services
* `udpTimeout`  timeout for UDP connections
* `claimInterval` payment claim interval for connections
* `subscriptionDuration` duration for subscription in blocks
* `subscriptionFee` fee used for subscription
* `services` services you want to provide
* `reverse` should be used if you don't have public IP and want to use another `server` for accepting clients
* `reverseRandomPorts` meaning reverse entry can use random ports instead of specified ones (useful when service has dynamic ports)
* `reverseMaxPrice` max accepted price for reverse service, unit is NKN per MB traffic
* `reverseNanoPayFee` nanoPay transaction fee for reverse service
* `reverseIPFilter` reverse service IP address filter

## Use as library

Most of them times you just need to run tuna entry/exit as a separate program
together with the services, but you can also use tuna as a library. See
[cmd/entry.go](cmd/entry.go) and [cmd/exit.go](cmd/exit.go) for examples.

## Compiling to iOS/Android native library

This library is designed to work with
[gomobile](https://godoc.org/golang.org/x/mobile/cmd/gomobile) and run natively
on iOS/Android without any modification. You can use `gomobile bind` to compile
it to Objective-C framework for iOS:

```shell
gomobile bind -target=ios -ldflags "-s -w" github.com/nknorg/tuna github.com/nknorg/nkn-sdk-go github.com/nknorg/nkngomobile
```

and Java AAR for Android:

```shell
gomobile bind -target=android -ldflags "-s -w" github.com/nknorg/tuna github.com/nknorg/nkn-sdk-go github.com/nknorg/nkngomobile
```

More likely you might want to write a simple wrapper uses tuna and compile it
using gomobile.

It's recommended to use the latest version of gomobile that supports go modules.

## Contributing

**Can I submit a bug, suggestion or feature request?**

Yes. Please open an issue for that.

**Can I contribute patches?**

Yes, we appreciate your help! To make contributions, please fork the repo, push
your changes to the forked repo with signed-off commits, and open a pull request
here.

Please sign off your commit. This means adding a line "Signed-off-by: Name
<email>" at the end of each commit, indicating that you wrote the code and have
the right to pass it on as an open source patch. This can be done automatically
by adding -s when committing:

```shell
git commit -s
```

## Community

- [Forum](https://forum.nkn.org/)
- [Discord](https://discord.gg/c7mTynX)
- [Telegram](https://t.me/nknorg)
- [Reddit](https://www.reddit.com/r/nknblockchain/)
- [Twitter](https://twitter.com/NKN_ORG)
