# TUNA 🐟

TUNA software is a decentralized, peer-to-peer networking software that enables users to share their unused network
bandwidth with others.
It is designed to create a more efficient and decentralized internet by allowing users to earn rewards for sharing their
resources.
The TUNA software is integrated with NKN blockchain technology, which allows for secure and transparent transactions
between users.
Based services through NKN. Node will receive payment for tunneled traffic
directly from user

## Build

Simply run `make` will do the work. The output binary name will be `tuna`.

## Get Started

Either entry mode or exit mode will need a service definition file
`services.json`. You can start by using `services.json.example` as template. The
service file defines what services to use or provide, which ports a service
uses, and various configurations like encryption.

## How to use

### Forward mode

If you want to use a forward proxy, whether it's an HTTP proxy or a SOCKS5 proxy,
you can configure it by modifying the `services.json` file from `services.json.example` and starting the TUNA entry.
By modifying this file, you can specify the type of proxy you want to use (i.e. HTTP or SOCKS5), as well as the port
listening on your local machine.

For example, the following configuration can be used to start TUNA entry client(forward mode)
The TUNA entry client automatically searches for TUNA entry servers that provide `httpproxy` services in the network,
and selects the node with the best speed for connection, then listens on the local port `30080`.
It provides TCP proxy services. With this functionality, the client can automatically find the best
available proxy server based on network conditions and provide optimal performance for the user.

Here is an example of `services.json`

```json
[
  {
    "name": "httpproxy",
    "tcp": [
      30080
    ],
    "encryption": "aes-gcm"
  }
]
```

Then you can start your entry client

You will need a config file `config.entry.json`. You can start by using
`config.entry.json.example` as template. You can just run the command below

```shell 
cp config.entry.json.example config.entry.json
```

Start tuna in entry mode:

```
./tuna entry
```

Then you can start using configured services as if they're on your local machine
(e.g. `127.0.0.1:30080` for HTTP proxy).

### Reverse mode

TUNA reverse mode is a reverse proxy. Exit is the internal service that need to be exposed to public Internet.
Entry is the service provider that provides public IP address.

For example, if you have a web server running on a local network, and you want to access it from the internet, you can
start TUNA exit to connect TUNA entry server. This way, when someone accesses the TUNA entry server with the IP address
and port that is
configured, TUNA will forward the request to the TUNA exit, then it will forward to the local web server.

Reverse mode is a powerful feature that allows users to access devices and services that are not directly accessible
from the internet, it can be useful for remote access to local resources, testing, and troubleshooting.

Here is an example of `services.json` for reverse clients which TUNA exit need

```json
[
  {
    "name": "httpproxy",
    "tcp": [
      30081
    ],
    "encryption": "xsalsa20-poly1305"
  }
]
```

You will need a config file `config.exit.json`. You can start by using
`config.exit.json.example` as template.

Set `reverse` to `true` in `config.exit.json` and start tuna in exit mode. Then
your service will be exposed to public network by reverse entry. Your node does
not need to have public IP address or open port at all.

If you want to use specific port on reverse entry, you need to set `reverseRandomPorts` == `true` in `config.exit.json`,
otherwise, you will be assigned to a random port.

If the client side does not set the TCP and UDP ports, then the TUNA server will automatically allocate random ports.

Start tuna in exit mode:

```
./tuna exit
```

## How to become a TUNA service provider and earn NKN from it

There are two ways for users to earn NKN by providing services using TUNA.

### Entry Mode

**The first way is to use the reverse mode of the entry to provide reverse proxy services**

```shell
./tuna -b=[YOUR_BENEFICIARY_ADDR] entry --reverse
```

Set `reverse` to `true` in `config.entry.json` and start tuna in entry mode.
Then users can use your node as reverse proxy and pay you NKN based on bandwidth
consumption.

Or you can set `reverseBeneficiaryAddr` in `config.entry.json`

You can also change the default listening port by setting `reverseTCP` & `reverseUDP` in entry config file.

Remember to set your price of your services by setting `reversePrice` default value is 0.0002 NKN per MB.

### Exit Mode

**The second way to earn revenue using TUNA is by using the forward mode of the exit to provide forward proxy services**

Here is an example of `services.json`

```json
[
  {
    "name": "httpproxy",
    "tcp": [
      30080
    ],
    "encryption": "xsalsa20-poly1305"
  },
  {
    "name": "socksproxy",
    "tcp": [
      30489
    ],
    "udp": [
      30489
    ],
    "encryption": "xsalsa20-poly1305"
  }
]
```

Then you can start your exit server by `./tuna -b=[YOUR_BENEFICIARY_ADDR] exit`

Don't forget to deploy your proxy services at port 30080 & 30489.

You can change the port as long as you ensure that the listening port is consistent with `services.json`

Then users can connect to your services through their tuna entry and pay you NKN
based on bandwidth consumption.

### Config

#### Entry mode config `config.entry.json`:

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

#### Exit mode config `config.exit.json`:

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
* `reverseRandomPorts` meaning reverse entry can use random ports instead of specified ones (useful when service has
  dynamic ports)
* `reverseMaxPrice` max accepted price for reverse service, unit is NKN per MB traffic
* `reverseNanoPayFee` nanoPay transaction fee for reverse service
* `reverseIPFilter` reverse service IP address filter

### encryption

TUNA supports AES and Salsa20 encryption algorithms, you can refer to the JSON configuration example above.

### Service filter

Users can configure several settings for the services offered by TUNA, such as setting a maximum price for the service,
creating IP whitelists and blacklists, creating a whitelist and blacklist of service provider public keys and specifying
the geographical location of the server. Remember using same service name in `services.json`
and `config.exit(or entry).json` when you set those settings
You can check `geo.IPFilter` and `filter.NknFilter` for more details.

## Use TUNA as library

Most of them times you just need to run tuna entry/exit as a separate program
together with the services, but you can also use tuna as a library. See
[tests/util.go](tests/util.go) for entry/exit & forward/reverse examples.

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
