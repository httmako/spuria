# spuria

Mini API server for executing bash on a remote server.  
The executable is only ~2MB in size (after using upx) and allows for IP whitelisting and prometheus "up" monitoring.  
Security is granted by IP whitelisting and calls/minute limits. It uses no external packages, only inbuilt golang ones.

There are 3 main HTTP codes used:

 - 200 ; successful execution of bash
 - 500 ; failed execution of bash
 - 404 ; URL not found


# Usecases

This was written for old servers who run on bare metal and have poorly written "order of startup" distributed servers code.  
Example:

An old java application runs on 3 different servers, but they have to be started in the correct order. This means you have to manually log in to all 3 servers and apply commands to each one, waiting until each one has started before going to the next server.  
With this application you could have 3 URLs on each server labeled `/status`, `/start` and `/stop`. This way you could remotely start/stop a service on a remote server and coordinate the startup/shutdown order from a central "main server" without having to implement a new framework / management layer.


# Build

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags '-w -s' .
# To make it smaller also do:
upx --best spuria
```

This will make the executable static and smaller thanks to upx (from 5.6MB to 1.9MB in my case).  
I read that upx may impact the startup time but in my test cases it was barely noticable if at all visible.


# Run

By default the application logs to stdout, listens on port 4870 and listens only on IP `127.0.0.1` (and only allows connections from `127.0.0.1`).  
The log is in the "logfmt" format, which can be parsed by Grafana Loki.  
To start it you have to provide a command to be executed. Examples:  

```bash
# This will listen on /do and print out "hi" with http code 200
./spuria -verbose -cmd "echo 'hi'"
# This will listen on the configured routes /test and /test2 and create files if accessed
./spuria -routes routes.example.csv
```


## Install upx

Make sure `~/.local/bin` is in your path.

```bash
cd ~/.local/bin
wget https://github.com/upx/upx/releases/download/v4.2.2/upx-4.2.2-amd64_linux.tar.xz
tar -xvf upx-4.2.2-amd64_linux.tar.xz
rm upx-4.2.2-amd64_linux.tar.xz
mv upx-4.2.2-amd64_linux/upx .
rm upx-4.2.2-amd64_linux/
```
