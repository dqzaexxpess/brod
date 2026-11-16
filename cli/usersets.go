package cli

import (
	"flag"
	"fmt"
	"net/url"
)

// brodctl sets — свои списки маршрутизации по URL (API /v1/route-sets).
func (a *app) sets(args []string) error {
	sub := "list"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "list", "":
		var r struct {
			Sets []map[string]any `json:"sets"`
		}
		if err := a.call("GET", "/v1/route-sets", nil, &r); err != nil {
			return err
		}
		if a.show(r) {
			return nil
		}
		if len(r.Sets) == 0 {
			fmt.Fprintln(a.env.Out, "своих списков нет (`brodctl sets add URL`)")
		}
		for _, s := range r.Sets {
			line := fmt.Sprintf("%s  %s  %s", str(s, "id"), humanBytes(num(s["size"])), str(s, "url"))
			if t := str(s, "title"); t != "" {
				line += "  «" + t + "»"
			}
			if e := str(s, "error"); e != "" {
				line += "  НЕ ОБНОВИЛСЯ: " + e
			}
			fmt.Fprintln(a.env.Out, line)
		}
	case "add":
		fs := flag.NewFlagSet("sets add", flag.ContinueOnError)
		fs.SetOutput(a.env.Err)
		title := fs.String("title", "", "название")
		reorder(fs, args)
		if fs.NArg() != 1 {
			return errUsage
		}
		var s map[string]any
		if err := a.call("POST", "/v1/route-sets", map[string]any{"url": fs.Arg(0), "title": *title}, &s); err != nil {
			return err
		}
		if !a.show(s) {
			fmt.Fprintf(a.env.Out, "добавлен %s (%s): в правилах условие rule_set: [\"%s\"]\n", str(s, "id"), humanBytes(num(s["size"])), str(s, "id"))
		}
	case "rm", "remove", "delete":
		if len(args) != 1 {
			return errUsage
		}
		if err := a.call("DELETE", "/v1/route-sets/"+url.PathEscape(args[0]), nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(a.env.Out, "удалён")
	default:
		return errUsage
	}
	return nil
}
