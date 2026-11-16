// serve-dir отдаёт каталог по http на 127.0.0.1 (для проверки обновления без публикации: update.url на этот же компьютер).
//
//	serve-dir [-addr 127.0.0.1:18765] [-dir .]
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:18765", "адрес (только этот компьютер)")
	dir := flag.String("dir", ".", "каталог")
	flag.Parse()
	log.Printf("отдаю %s на http://%s", *dir, *addr)
	log.Fatal(http.ListenAndServe(*addr, http.FileServer(http.Dir(*dir))))
}
