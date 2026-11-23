// otasign — ключи и подписи для обновлений (пакет ota). Закрытый ключ остаётся у издателя; в репозиторий попадает только подпись.
//
//	otasign keygen [-out ~/.config/brod/ota-signing.key]   создать пару ключей (файл 0600, печатает открытый ключ для ota.PublicKey)
//	otasign pub    [-key файл]                              напечатать открытый ключ
//	otasign sign   [-key файл] ФАЙЛ                         записать ФАЙЛ.sig (base64) рядом с файлом
//	otasign verify [-key файл] ФАЙЛ                         проверить ФАЙЛ.sig открытым ключом, вшитым в программу (или -pub base64)
//	otasign check  ФАЙЛ                                     проверить список источников так же строго, как клиент (подпись не нужна)
//	otasign release -version N -app 0.4.0 -asset ПЛАТФОРМА=ФАЙЛ@URL [-asset …] [-mirror ПЛАТФОРМА=URL] [-notes ТЕКСТ|@ФАЙЛ] [-o releases.json]
//	                                                        собрать манифест релизов: SHA-256 и размер берутся из ФАЙЛА, адрес — URL, где его
//	                                                        опубликуют; платформы: android-arm64, linux-amd64, windows-amd64; подписать — sign
//	otasign build  -version N [-from sources.txt] [-defaults] [-retired a,b] [-out sources.json]
//	                                                        собрать список источников из файла «имя|url|приоритет» (и вшитых источников
//	                                                        по умолчанию), проверить так же строго, как клиент; подписать — командой sign
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"brod/aggregator"
	"brod/ota"
)

// defaultKeyFile: ключ списка источников и ключ релизов раздельны (компрометация одного не даёт подписать второе).
func defaultKeyFile(releases bool) string {
	h, _ := os.UserHomeDir()
	name := "ota-signing.key"
	if releases {
		name = "releases-signing.key"
	}
	return filepath.Join(h, ".config", "brod", name)
}

func defaultKey() string { return defaultKeyFile(false) }

// isReleases: файл — манифест релизов (по имени: releases.json), он подписывается и проверяется ключом релизов.
func isReleases(path string) bool { return filepath.Base(path) == ota.ReleasesFile }

func load(path string) (ed25519.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%s: неверный файл ключа (ожидается base64 от 32 байт)", path)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: otasign keygen|pub|sign|verify")
		os.Exit(2)
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	forRel := fs.Bool("releases", false, "ключ релизов (keygen, pub): файл по умолчанию releases-signing.key; sign и verify выбирают ключ по имени файла")
	key := fs.String("key", "", "файл закрытого ключа (по умолчанию ota-signing.key, для releases.json — releases-signing.key)")
	out := fs.String("out", "", "куда записать новый ключ (keygen; по умолчанию ota-signing.key или с -releases releases-signing.key)")
	pub := fs.String("pub", "", "открытый ключ base64 (verify; по умолчанию вшитый)")
	version := fs.Int("version", 0, "номер версии списка (build; должен быть больше уже опубликованного)")
	from := fs.String("from", "", "файл «имя|url|приоритет» (build)")
	defaults := fs.Bool("defaults", false, "добавить встроенные источники по умолчанию (build)")
	retired := fs.String("retired", "", "через запятую: источники, которые клиенты должны отключить (build)")
	outFile := fs.String("o", "sources.json", "куда записать список (build)")
	app := fs.String("app", "", "версия приложения 1.2.3 (release)")
	loopback := fs.Bool("loopback", false, "release: допустить http-адреса на этот же компьютер (только для проверки обновления с локального сервера через настройку update.url; публиковать такой манифест нельзя)")
	notes := fs.String("notes", "", "описание релиза: текст или @файл (release)")
	var assets, mirrors multi
	fs.Var(&assets, "asset", "ПЛАТФОРМА=ФАЙЛ@URL (release; можно несколько)")
	fs.Var(&mirrors, "mirror", "ПЛАТФОРМА=URL запасного адреса (release)")
	_ = fs.Parse(os.Args[2:])
	if *out == "" {
		*out = defaultKeyFile(*forRel)
	}
	keyFor := func(file string) string { // закрытый ключ для подписи file
		if *key != "" {
			return *key
		}
		return defaultKeyFile(*forRel || (file != "" && isReleases(file)))
	}
	switch os.Args[1] {
	case "keygen":
		if _, err := os.Stat(*out); err == nil {
			fatal(fmt.Errorf("%s уже есть: прежний ключ не перезаписывается (потеря ключа = выпуск приложений с новым открытым ключом)", *out))
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		fatal(err)
		fatal(os.MkdirAll(filepath.Dir(*out), 0o700))
		fatal(os.WriteFile(*out, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600))
		fmt.Println("открытый ключ (в ota.PublicKey):", base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
		fmt.Println("закрытый ключ:", *out, "— сделайте резервную копию отдельно, в репозиторий не кладите")
	case "pub":
		priv, err := load(keyFor(""))
		fatal(err)
		fmt.Println(base64.StdEncoding.EncodeToString(priv.Public().(ed25519.PublicKey)))
	case "sign":
		if fs.NArg() != 1 {
			fatal(fmt.Errorf("нужен один файл"))
		}
		priv, err := load(keyFor(fs.Arg(0)))
		fatal(err)
		doc, err := os.ReadFile(fs.Arg(0))
		fatal(err)
		fatal(os.WriteFile(fs.Arg(0)+".sig", []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(priv, doc))+"\n"), 0o644))
		fmt.Println("подписано:", fs.Arg(0)+".sig")
	case "verify":
		if fs.NArg() != 1 {
			fatal(fmt.Errorf("нужен один файл"))
		}
		pks := []ed25519.PublicKey{ota.DefaultKey()}
		if isReleases(fs.Arg(0)) {
			pks = ota.ReleasesKeys()
		}
		if *pub != "" {
			b, err := base64.StdEncoding.DecodeString(*pub)
			if err != nil || len(b) != ed25519.PublicKeySize {
				fatal(fmt.Errorf("неверный открытый ключ"))
			}
			pks = []ed25519.PublicKey{ed25519.PublicKey(b)}
		}
		doc, err := os.ReadFile(fs.Arg(0))
		fatal(err)
		sig, err := os.ReadFile(fs.Arg(0) + ".sig")
		fatal(err)
		fatal(ota.VerifyAny(pks, doc, sig))
		fmt.Println("подпись верна")
	case "release":
		doc := ota.ReleasesDoc{Version: *version, Created: time.Now().UTC().Format(time.RFC3339), Release: ota.Release{App: *app, Assets: map[string]ota.Asset{}}}
		if strings.HasPrefix(*notes, "@") {
			b, err := os.ReadFile((*notes)[1:])
			fatal(err)
			doc.Release.Notes = strings.TrimSpace(string(b))
		} else {
			doc.Release.Notes = *notes
		}
		for _, a := range assets {
			plat, rest, ok := strings.Cut(a, "=")
			file, link, ok2 := strings.Cut(rest, "@")
			if !ok || !ok2 || plat == "" || file == "" || link == "" {
				fatal(fmt.Errorf("-asset %q: нужен вид ПЛАТФОРМА=ФАЙЛ@URL", a))
			}
			f, err := os.Open(file)
			fatal(err)
			h := sha256.New()
			n, err := io.Copy(h, f)
			f.Close()
			fatal(err)
			doc.Release.Assets[plat] = ota.Asset{URL: link, SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}
		}
		for _, m := range mirrors {
			plat, link, ok := strings.Cut(m, "=")
			a, has := doc.Release.Assets[plat]
			if !ok || !has {
				fatal(fmt.Errorf("-mirror %q: нужен вид ПЛАТФОРМА=URL для платформы, заданной через -asset", m))
			}
			a.Mirror = link
			doc.Release.Assets[plat] = a
		}
		b, err := json.MarshalIndent(doc, "", " ")
		fatal(err)
		b = append(b, '\n')
		_, err = ota.ParseReleasesWith(b, *loopback) // клиент отвергнет всё, что не пройдёт эту проверку
		fatal(err)
		fatal(os.WriteFile(*outFile, b, 0o644))
		fmt.Printf("собрано: %s (публикация %d, приложение %s, платформ %d); дальше: otasign sign %s\n", *outFile, doc.Version, doc.Release.App, len(doc.Release.Assets), *outFile)
	case "check":
		if fs.NArg() != 1 {
			fatal(fmt.Errorf("нужен один файл"))
		}
		b, err := os.ReadFile(fs.Arg(0))
		fatal(err)
		if filepath.Base(fs.Arg(0)) == ota.ReleasesFile { // манифест релизов проверяется своим разбором
			r, err := ota.ParseReleases(b)
			fatal(err)
			fmt.Printf("манифест корректен: публикация %d, приложение %s, платформ %d\n", r.Version, r.Release.App, len(r.Release.Assets))
			return
		}
		d, err := ota.ParseSources(b)
		fatal(err)
		fmt.Printf("список корректен: версия %d, источников %d, снятых %d\n", d.Version, len(d.Sources), len(d.Retired))
	case "build":
		var srcs []aggregator.Source
		if *defaults {
			srcs = append(srcs, aggregator.Defaults()...)
		}
		if *from != "" {
			extra, err := aggregator.ParseSourcesFile(*from)
			fatal(err)
			srcs = append(srcs, extra...)
		}
		doc := ota.SourcesDoc{Version: *version, Created: time.Now().UTC().Format(time.RFC3339)}
		seen := map[string]int{}
		for _, s := range srcs {
			e := ota.SourceEntry{Name: s.Name, URL: s.URL, Mirror: s.Mirror, Priority: s.Priority}
			if i, dup := seen[s.Name]; dup { // позднее определение заменяет раннее (файл сильнее встроенных)
				doc.Sources[i] = e
				continue
			}
			seen[s.Name] = len(doc.Sources)
			doc.Sources = append(doc.Sources, e)
		}
		for _, n := range strings.Split(*retired, ",") {
			if n = strings.TrimSpace(n); n != "" {
				doc.Retired = append(doc.Retired, n)
			}
		}
		b, err := json.MarshalIndent(doc, "", " ")
		fatal(err)
		b = append(b, '\n')
		_, err = ota.ParseSources(b) // клиент отвергнет всё, что не пройдёт эту проверку
		fatal(err)
		fatal(os.WriteFile(*outFile, b, 0o644))
		fmt.Printf("собрано: %s (версия %d, источников %d, снятых %d); дальше: otasign sign %s\n", *outFile, doc.Version, len(doc.Sources), len(doc.Retired), *outFile)
	default:
		fatal(fmt.Errorf("неизвестная команда %s", os.Args[1]))
	}
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func fatal(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}
