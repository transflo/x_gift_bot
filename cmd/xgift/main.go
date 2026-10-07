package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

	"xgift/internal/accounts"
	"xgift/internal/checkout"
	"xgift/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "xgift:", err)
		os.Exit(1)
	}
}

func run() error {
	args := os.Args[1:]
	command := ""
	positional := []string{}
	rest := []string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			if a == "--db" || a == "--name" || a == "--months" || a == "--id" || a == "--label" {
				i++
				if i >= len(args) {
					return errors.New("missing flag value")
				}
				rest = append(rest, args[i])
			}
		} else if command == "" {
			command = a
		} else {
			positional = append(positional, a)
		}
	}
	f := flag.NewFlagSet("xgift", flag.ContinueOnError)
	defaultDB := "sqlite/records.db"
	if exe, e := os.Executable(); e == nil {
		if resolved, e := filepath.EvalSymlinks(exe); e == nil {
			candidate := filepath.Join(filepath.Dir(resolved), "..", "sqlite", "records.db")
			if _, e = os.Stat(candidate); e == nil {
				defaultDB = candidate
			}
		}
	}
	db := f.String("db", defaultDB, "SQLite record store")
	months := f.Int("months", 6, "gift duration in months; must match a plan in the catalog record")
	name := f.String("name", "", "secret name for put")
	id := f.String("id", "", "account id for accounts commands")
	label := f.String("label", "", "account label when adding")
	f.Usage = func() {
		fmt.Fprintln(f.Output(), "Usage: xgift <setup|init|status|put|accounts|check|link> [flags]\nsetup is the interactive first-time wizard; init reads a JSON object from stdin; put writes one record (--name accounts|api-auth|stripe-key|catalog). accounts list|add|remove|enable|disable|test manages the X account pool and its Shadowsocks proxies. link creates or reuses a Stripe payment link for one X username.")
		f.PrintDefaults()
	}
	if err := f.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if command == "" {
		f.Usage()
		return nil
	}
	if command == "setup" {
		return runSetup(context.Background(), *db)
	}
	if command == "init" {
		input, err := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
		if err != nil {
			return err
		}
		defer clear(input)
		var records map[string]json.RawMessage
		if err = json.Unmarshal(input, &records); err != nil {
			return errors.New("stdin must contain a JSON object of secrets")
		}
		if len(records) == 0 {
			return errors.New("no secrets supplied")
		}
		if _, err = os.Stat(*db); !os.IsNotExist(err) {
			return errors.New("record store already exists or path is inaccessible")
		}
		v, err := store.Open(*db)
		if err != nil {
			return err
		}
		defer v.Close()
		for n, b := range records {
			// A JSON string secret (for example the Stripe key) is stored as its
			// decoded value; structured records keep their JSON form.
			if len(b) > 0 && b[0] == '"' {
				var s string
				if err = json.Unmarshal(b, &s); err != nil {
					return fmt.Errorf("record %s: invalid JSON string", n)
				}
				b = []byte(s)
			}
			if err = validateRecord(n, b); err != nil {
				return fmt.Errorf("record %s: %w", n, err)
			}
			if err = v.Put(n, b); err != nil {
				return err
			}
		}
		fmt.Printf("Record store created: %s\n", *db)
		return nil
	}
	v, err := store.Open(*db)
	if err != nil {
		return err
	}
	defer v.Close()

	switch command {
	case "accounts":
		sub := ""
		if len(positional) > 0 {
			sub = positional[0]
		}
		return runAccounts(v, sub, *id, *label)
	case "check":
		return runCheck(v)
	case "status":
		return runStatus(v)
	case "put":
		return runPut(v, *name)
	case "link":
		if len(positional) != 1 || !regexp.MustCompile(`^@?[A-Za-z0-9_]{1,15}$`).MatchString(positional[0]) {
			return errors.New("link expects an X username")
		}
		return runLink(v, *db, positional[0], *months)
	default:
		return errors.New("unknown command; run xgift for usage")
	}
}

func validateRecord(name string, b []byte) error {
	switch name {
	case "accounts":
		_, err := accounts.Parse(b)
		return err
	case "api-auth":
		var auth struct{ Authorization string }
		if json.Unmarshal(b, &auth) != nil || !strings.HasPrefix(auth.Authorization, "Bearer ") {
			return errors.New("api-auth must be {\"Authorization\":\"Bearer ...\",\"UserAgent\":\"...\"}")
		}
		return nil
	case "stripe-key":
		if !checkout.ValidStripeKey(string(b)) {
			return errors.New("stdin must be the pk_live_ publishable key")
		}
		return nil
	case "catalog":
		_, err := checkout.ParseCatalog(b)
		return err
	default:
		return errors.New("unsupported record name")
	}
}

func runStatus(v *store.Store) error {
	list, err := accounts.Load(v)
	if err != nil {
		return err
	}
	enabled := 0
	for _, a := range list {
		if a.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return accounts.ErrNoEnabledAccount
	}
	fmt.Printf("accounts: %d record(s), %d enabled\n", len(list), enabled)
	if _, err = v.Get("api-auth"); err != nil {
		return errors.New("record api-auth is missing; fix with put --name api-auth")
	}
	fmt.Println("api-auth: record verified")
	key, err := v.Get("stripe-key")
	if err != nil {
		return errors.New("record stripe-key is missing; fix with put --name stripe-key")
	}
	if !checkout.ValidStripeKey(string(key)) {
		return errors.New("invalid stripe-key record; rewrite with put --name stripe-key")
	}
	fmt.Println("stripe-key: record verified")
	if _, err = checkout.ReadCatalog(v); err != nil {
		return err
	}
	fmt.Println("catalog: record verified")
	return nil
}

func runCheck(v *store.Store) error {
	list, err := accounts.Load(v)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	mgr := accounts.NewManager(ctx)
	defer mgr.Close()
	failed := false
	for _, a := range list {
		if !a.Enabled {
			continue
		}
		fmt.Printf("%s … ", accounts.Redact(a))
		checkCtx, stop := context.WithCancel(ctx)
		checkErr := mgr.Check(checkCtx, a)
		stop()
		if checkErr != nil {
			fmt.Printf("失败：%v\n", checkErr)
			failed = true
			continue
		}
		fmt.Println("可访问 X")
	}
	if failed {
		return errors.New("one or more accounts could not reach X through their proxy")
	}
	return nil
}

func runAccounts(v *store.Store, sub, id, label string) error {
	list, err := accounts.Load(v)
	if err != nil {
		list = []accounts.Account{}
	}
	switch sub {
	case "", "list":
		for i, a := range list {
			state := "启用"
			if !a.Enabled {
				state = "停用"
			}
			fmt.Printf("%d) %s [%s] %s → %s:%d (%s)\n", i+1, a.ID, state, a.Label, a.Proxy.Server, a.Proxy.ServerPort, a.Proxy.Method)
		}
		if len(list) == 0 {
			fmt.Println("账号池为空。")
		}
		return nil
	case "add":
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 1024*1024))
		if err != nil {
			return err
		}
		defer clear(b)
		incoming, err := accounts.Parse(b)
		if err != nil {
			return err
		}
		if strings.TrimSpace(label) != "" && len(incoming) == 1 {
			incoming[0].Label = strings.TrimSpace(label)
		}
		list = append(list, incoming...)
		if err = accounts.Save(v, list); err != nil {
			return err
		}
		fmt.Printf("账号池已保存：共 %d 个账号。\n", len(list))
		return nil
	case "remove":
		if id == "" {
			return errors.New("accounts remove requires --id")
		}
		next := list[:0]
		found := false
		for _, a := range list {
			if a.ID == id {
				found = true
				continue
			}
			next = append(next, a)
		}
		if !found {
			return errors.New("account not found")
		}
		if len(next) == 0 {
			return errors.New("at least one account must remain")
		}
		if err = accounts.Save(v, next); err != nil {
			return err
		}
		fmt.Printf("已删除账号 %s；账号池剩余 %d 个\n", id, len(next))
		return nil
	case "enable", "disable":
		if id == "" {
			return fmt.Errorf("accounts %s requires --id", sub)
		}
		found := false
		for i := range list {
			if list[i].ID == id {
				list[i].Enabled = sub == "enable"
				found = true
				break
			}
		}
		if !found {
			return errors.New("account not found")
		}
		if err = accounts.Save(v, list); err != nil {
			return err
		}
		fmt.Printf("账号 %s 已%s\n", id, map[string]string{"enable": "启用", "disable": "停用"}[sub])
		return nil
	case "test":
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		mgr := accounts.NewManager(ctx)
		defer mgr.Close()
		failed := false
		for _, a := range list {
			if id != "" && a.ID != id {
				continue
			}
			fmt.Printf("%s … ", accounts.Redact(a))
			checkErr := mgr.Check(ctx, a)
			if checkErr != nil {
				fmt.Printf("失败：%v\n", checkErr)
				failed = true
				continue
			}
			fmt.Println("可访问 X")
		}
		if failed {
			return errors.New("one or more accounts could not reach X")
		}
		return nil
	default:
		return errors.New("accounts expects list, add, remove --id, enable --id, disable --id or test [--id]")
	}
}

func runPut(v *store.Store, name string) error {
	if name == "" {
		return errors.New("--name is required")
	}
	limit := int64(1024 * 1024)
	if name == "stripe-key" {
		limit = 4096
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, limit))
	if err != nil {
		return err
	}
	defer clear(b)
	b = []byte(strings.TrimSpace(string(b)))
	if err = validateRecord(name, b); err != nil {
		return err
	}
	if err = v.Put(name, b); err != nil {
		return err
	}
	fmt.Printf("%s saved\n", name)
	return nil
}

func runLink(v *store.Store, db string, user string, months int) error {
	user = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(user), "@"))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(db), "checkout.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("another checkout command is running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	mgr := accounts.NewManager(ctx)
	defer mgr.Close()
	record, err := checkout.CreateAdminLink(ctx, v, mgr, user, months)
	if record != nil {
		fmt.Println(checkout.CheckoutLink(record))
		fmt.Fprintf(os.Stderr, "Checkout status: %s\n", record.Status)
	}
	return err
}
