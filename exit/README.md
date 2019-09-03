## Build
Simply run:
```shell
glide install
go build exit.go
```

## How to use
Edit `config.json` with your data:
```json
{
  "ListenTCP": 30004,
  "ListenUDP": 30005,
  "Reverse": false,
  "DialTimeout": 30,
  "UDPTimeout": 60,
  "Seed": "",
  "SubscriptionPrefix": "tuna+1.",
  "SubscriptionDuration": 60,
  "ClaimInterval": 60,
  "Services": {
    "httpproxy": {
      "address": "127.0.0.1",
      "price": "0.001"
    },
    "moonlight": {
      "address": "127.0.0.1",
      "price": "0.001"
    }
  }
}
```
`ListenTCP` TCP port to listen for connections  
`ListenUDP` UDP port to listen for connections  
`Reverse` should be used if you don't have public IP and want to use another `server` for accepting clients  
`DialTimeout` timeout for connections to services  
`UDPTimeout`  timeout for UDP *connections*  
`Seed` your seed  
`SubscriptionPrefix` prefix appended to topics for subscription  
`SubscriptionDuration` duration for subscription in blocks  
`ClaimInterval` payment claim interval for connections  
`Services` services you want to provide  

Run like this:
```shell
./exit
```

Then users can connect to your services over NKN through their *tuna* client
