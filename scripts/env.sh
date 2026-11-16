# source scripts/env.sh — окружение Go для проекта.
# proxy.golang.org и dl.google.com режутся с текущего выхода, поэтому goproxy.cn;
# целостность проверяет sumdb (GOSUMDB по умолчанию), `direct` — запасной путь для VCS.
export GOPROXY=https://goproxy.cn,direct
export GOTOOLCHAIN=auto
export GOFLAGS=-buildvcs=false
export PATH="$HOME/dev/vpn-client/bin:$PATH"
