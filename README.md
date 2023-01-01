# TapMon

## What
Tapmon polls Tapo P110 Smart Plugs for energy usage data and uses RemoteWrite to push time-series data to a compatible 
endpoint such as Prometheus or Grafana Cloud.

## How
```bash
$ go build -o tapmon .
$ ./tapmon config.yaml
```

## Config
```yaml
# config.yaml

interval: 60

prometheus:
  username: user
  password: pass
  endpoint: https://endpoint/api/prom/push
  flushInterval: 60



devices:
  - ip: 192.168.1.69
    username: user@domain.tld
    password: thepassword

  - ip: 192.168.1.70
    username: user@domain.tld
    password: thepassword
```


## Systemd Unit Example

`/etc/systemd/system/tapmon.service`
```
[Unit]
Description=Tapmon
After=network.target

[Service]
StandardError=journal
StandardOutput=journal
Environment="TAPMON_LOGLEVEL=info"
ExecStart=/opt/tapmon/bin/tapmon /opt/tapmon/etc/config.yaml

[Install]
WantedBy=multi-user.target
```