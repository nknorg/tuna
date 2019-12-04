# TUNA 🐟

TUNA (Tunnel Using NKN for any Application) allows anyone to tunnel any network
based services through NKN. Node will receive payment for tunneled traffic
directly from user.

## Tuna Entry

### Build

Simply run:
```shell
go build -o entry bin/entry/main.go
```

### How to use

Edit `config.entry.json` with your data:

* `DialTimeout` timeout for NKN node connection  
* `UDPTimeout`  timeout for UDP *connections*  
* `Services` services you want to use  
* `NanoPayFee` fee used for nano pay transaction  
* `Reverse` should be used to provide reverse tunnel for those who don't have public IP  
* `ReverseBeneficiaryAddr` Beneficiary address (NKN wallet address to receive rewards)
* `ReverseTCP` TCP port to listen for connections  
* `ReverseUDP` UDP port to listen for connections  
* `ReversePrice` price for reverse connections  
* `ReverseSubscriptionPrefix` prefix appended to topics for subscription  
* `ReverseSubscriptionDuration` duration for subscription in blocks  
* `ReverseSubscriptionFee` fee used for subscription  
* `ReverseClaimInterval` payment claim interval for reverse connections  

Start tuna entry:
```shell
./entry
```

Then you can start using configured services as if they're on your local machine (e.g. `127.0.0.1:30080` for HTTP proxy)

## Tuna Exit

### Build

Simply run:
```shell
go build -o exit bin/exit/main.go
```

### How to use

Edit `config.exit.json` with your data:

* `BeneficiaryAddr` Beneficiary address (NKN wallet address to receive rewards)
* `ListenTCP` TCP port to listen for connections  
* `ListenUDP` UDP port to listen for connections  
* `Reverse` should be used if you don't have public IP and want to use another `server` for accepting clients  
* `ReverseRandomPorts` meaning reverse entry can use random ports instead of specified ones (useful when service has dynamic ports)  
* `DialTimeout` timeout for connections to services  
* `UDPTimeout`  timeout for UDP *connections*  
* `SubscriptionPrefix` prefix appended to topics for subscription  
* `SubscriptionDuration` duration for subscription in blocks  
* `SubscriptionFee` fee used for subscription  
* `ClaimInterval` payment claim interval for connections  
* `Services` services you want to provide  

Start tuna exit:
```shell
./exit
```

Then users can connect to your services over NKN through their *tuna* client

### Specifying service ports programmatically

In case you're running services with dynamic ports it may be inconvenient to specify port in the `services.json` before each launch.  
Instead you can run TUNA's exit programmatically, launch your service and then provide it's port to TUNA.

```go
...
// read/prepare config
// init wallet sdk
// launch your service
port := ... // port of the newly launched service
...
serviceName := "proxy"
services := []tuna.Service{{
    Name: serviceName,
    TCP:  []int{port},
}}
exit := tuna.NewTunaExit(config, services, wallet) // can be used only once-per-service
exit.StartReverse(serviceName)

select {} // prevent TUNA from exiting
```

### Getting dynamic ports

When some of the ports of the service is specified as 0, then entry will use random ports for them, here's how you can check which ports it's actually using:
```go
exit.OnEntryConnected(func() {
    ip := exit.GetReverseIP()
    tcpPorts := exit.GetReverseTCPPorts()
    udpPorts := exit.GetReverseUDPPorts()
})
```
Returned ports will correspond to local ports specified in the same order.  
This information will only be available after connection with the reverse entry has been made.
