## Build
Simply run:
```shell
glide install
go build entry.go
```

## How to use
Edit `config.json` with your data:
```json
{
  "DialTimeout": 30,
  "UDPTimeout": 60,
  "PrivateKey": "",
  "Services": ["httpproxy", "moonlight"],

  "Reverse": false,
  "ReverseTCP": 40004,
  "ReverseUDP": 40005,
  "SubscriptionDuration": 60,
  "SubscriptionInterval": 20
}
```
`DialTimeout` timeout for NKN node connection  
`UDPTimeout`  timeout for UDP *connections*  
`PrivateKey` your private key  
`Services` services you want to use  

`Reverse` should be used to provide reverse tunnel for those who don't have public IP  
`ReverseTCP` TCP port to listen for connections  
`ReverseUDP` UDP port to listen for connections  
`SubscriptionDuration` duration for subscription in blocks  
`SubscriptionInterval` interval for subscription in seconds  

Run like this:
```shell
./entry
```

Then you can start using configured services as if they're on your local machine (e.g. `127.0.0.1:8888` for HTTP proxy)
