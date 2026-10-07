package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"xgift/internal/accounts"
	"xgift/internal/checkout"
	"xgift/internal/store"
)

type wizard struct {
	r   *bufio.Reader
	tty bool
}

func (w *wizard) line() (string, error) {
	s, err := w.r.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && s != "") {
		return "", errors.New("读取输入失败")
	}
	return strings.TrimRight(s, "\r\n"), nil
}

// prompt reads one line; empty input falls back to def.
func (w *wizard) prompt(label, def string) (string, error) {
	if def != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	s, err := w.line()
	if err != nil {
		return "", err
	}
	if s == "" {
		return def, nil
	}
	return s, nil
}

// secret hides input on a terminal; piped stdin is read as plain lines.
func (w *wizard) secret(label string) (string, error) {
	fmt.Printf("%s: ", label)
	if w.tty {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", errors.New("读取输入失败")
		}
		return string(b), nil
	}
	return w.line()
}

func (w *wizard) yesNo(label string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	s, err := w.prompt(fmt.Sprintf("%s [%s]", label, hint), "")
	if err != nil {
		return false, err
	}
	if s == "" {
		return def, nil
	}
	switch strings.ToLower(s) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	}
	return false, errors.New("请输入 y 或 n")
}

func writeOwnerOnly(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// runSetup is the interactive first-time wizard; it refuses to touch an
// existing record store so updates keep going through put/accounts.
func runSetup(ctx context.Context, db string) error {
	w := &wizard{r: bufio.NewReader(os.Stdin), tty: term.IsTerminal(int(os.Stdin.Fd()))}
	fmt.Println("XGift 配置向导")
	fmt.Println("本向导会依次配置：X 账号池（每个账号一个 Shadowsocks 代理）、Stripe 公钥、套餐目录和站点。")
	if err := os.MkdirAll(filepath.Dir(db), 0700); err != nil {
		return err
	}
	v, err := store.Open(db)
	if err != nil {
		return err
	}
	defer v.Close()
	fmt.Printf("已创建记录库：%s\n\n", db)

	if err = w.setupXAPI(v); err != nil {
		return err
	}
	if err = w.setupAccounts(ctx, v); err != nil {
		return err
	}
	if err = w.setupStripeKey(v); err != nil {
		return err
	}
	if err = w.setupCatalog(v); err != nil {
		return err
	}
	return w.setupSite(filepath.Dir(db))
}

func (w *wizard) setupXAPI(v *store.Store) error {
	fmt.Println("【1/5】X API 授权")
	fmt.Println("直接回车使用内置的公开 Web Bearer（与网页版一致）。")
	bearer, err := w.prompt("Authorization Bearer", checkout.DefaultAPIAuthorization)
	if err != nil {
		return err
	}
	ua, err := w.prompt("User-Agent", checkout.DefaultAPIUserAgent)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(bearer, "Bearer ") {
		bearer = "Bearer " + bearer
	}
	data, _ := json.Marshal(map[string]string{"Authorization": bearer, "UserAgent": ua})
	defer clear(data)
	return v.Put("api-auth", data)
}

// setupAccounts collects one or more X identities, each bound to exactly one
// Shadowsocks proxy.
func (w *wizard) setupAccounts(ctx context.Context, v *store.Store) error {
	fmt.Println("\n【2/5】X 账号池")
	fmt.Println("每个账号需要一个 auth_token、ct0 和一个 Shadowsocks 代理（一个账号对应一个代理）。")
	fmt.Println("浏览器登录 x.com 后，在开发者工具 → Application → Cookies 中复制 auth_token 与 ct0。")
	mgr := accounts.NewManager(ctx)
	defer mgr.Close()
	var list []accounts.Account
	for {
		label, err := w.prompt(fmt.Sprintf("账号 %d 名称", len(list)+1), fmt.Sprintf("账号%d", len(list)+1))
		if err != nil {
			return err
		}
		auth, err := w.secret("auth_token")
		if err != nil {
			return err
		}
		ct0, err := w.secret("ct0")
		if err != nil {
			return err
		}
		server, err := w.prompt("Shadowsocks 服务器地址（IP 或域名）", "")
		if err != nil {
			return err
		}
		portText, err := w.prompt("Shadowsocks 端口", "8388")
		if err != nil {
			return err
		}
		port := 0
		if _, err = fmt.Sscanf(portText, "%d", &port); err != nil || port < 1 || port > 65535 {
			return errors.New("端口必须是 1..65535")
		}
		method, err := w.prompt("加密方式", "aes-256-gcm")
		if err != nil {
			return err
		}
		password, err := w.secret("代理密码")
		if err != nil {
			return err
		}
		account := accounts.Account{Label: strings.TrimSpace(label), AuthToken: strings.TrimSpace(auth), CT0: strings.TrimSpace(ct0), Enabled: true, Proxy: accounts.Proxy{Type: "shadowsocks", Server: strings.TrimSpace(server), ServerPort: port, Method: strings.TrimSpace(method), Password: password}}
		if err = account.Validate(); err != nil {
			fmt.Printf("配置无效：%v，请重新输入。\n", err)
			continue
		}
		fmt.Print("正在测试代理连通性… ")
		checkCtx, cancel := context.WithTimeout(ctx, 30*1e9)
		checkErr := mgr.Check(checkCtx, account)
		cancel()
		if checkErr != nil {
			fmt.Printf("失败：%v\n", checkErr)
			retry, err := w.yesNo("仍然保存该账号（可稍后在后台修改）", false)
			if err != nil {
				return err
			}
			if !retry {
				continue
			}
		} else {
			fmt.Println("成功")
		}
		list = append(list, account)
		more, err := w.yesNo("继续添加下一个 X 账号", false)
		if err != nil {
			return err
		}
		if !more {
			break
		}
	}
	if len(list) == 0 {
		return errors.New("至少需要一个 X 账号")
	}
	return accounts.Save(v, list)
}

func (w *wizard) setupStripeKey(v *store.Store) error {
	fmt.Println("\n【3/5】Stripe 公钥")
	fmt.Println("填写 X 结账页使用的 pk_live_ 公钥。")
	for {
		key, err := w.prompt("Stripe 公钥", "")
		if err != nil {
			return err
		}
		key = strings.TrimSpace(key)
		if checkout.ValidStripeKey(key) {
			return v.Put("stripe-key", []byte(key))
		}
		fmt.Println("格式不正确，应以 pk_live_ 开头。")
	}
}

func (w *wizard) setupCatalog(v *store.Store) error {
	fmt.Println("\n【4/5】套餐目录")
	useDefault, err := w.yesNo("使用 X Premium 默认目录（3/6 个月）", true)
	if err != nil {
		return err
	}
	var catalog checkout.Catalog
	if useDefault {
		catalog = checkout.DefaultCatalog()
	} else {
		catalog, err = w.customCatalog()
		if err != nil {
			return err
		}
	}
	data, _ := json.Marshal(catalog)
	defer clear(data)
	if _, err = checkout.ParseCatalog(data); err != nil {
		return err
	}
	return v.Put("catalog", data)
}

func (w *wizard) customCatalog() (checkout.Catalog, error) {
	merchant, err := w.prompt("Stripe 商户号（acct_ 开头）", checkout.DefaultXMerchant)
	if err != nil {
		return checkout.Catalog{}, err
	}
	currency, err := w.prompt("币种（三位小写代码）", checkout.DefaultXCurrency)
	if err != nil {
		return checkout.Catalog{}, err
	}
	catalog := checkout.Catalog{Merchant: strings.TrimSpace(merchant), Currency: strings.ToLower(strings.TrimSpace(currency))}
	for {
		monthsText, err := w.prompt(fmt.Sprintf("第 %d 个套餐的月数（回车结束）", len(catalog.Plans)+1), "")
		if err != nil {
			return checkout.Catalog{}, err
		}
		if monthsText == "" {
			break
		}
		months := 0
		if _, err = fmt.Sscanf(monthsText, "%d", &months); err != nil {
			return checkout.Catalog{}, errors.New("月数必须是数字")
		}
		amountText, err := w.prompt("金额（最小货币单位，例如 259900 表示 2599.00）", "")
		if err != nil {
			return checkout.Catalog{}, err
		}
		amount := 0
		if _, err = fmt.Sscanf(amountText, "%d", &amount); err != nil {
			return checkout.Catalog{}, errors.New("金额必须是整数")
		}
		product, err := w.prompt("X 商品 ID（prod_ 开头）", "")
		if err != nil {
			return checkout.Catalog{}, err
		}
		catalog.Plans = append(catalog.Plans, checkout.CatalogPlan{Months: months, Amount: amount, Product: strings.TrimSpace(product)})
		if len(catalog.Plans) == 2 {
			break
		}
	}
	return catalog, nil
}

func (w *wizard) setupSite(dir string) error {
	fmt.Println("\n【5/5】站点配置")
	origin, err := w.prompt("站点完整域名（https:// 开头）", "https://example.com")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(origin, "https://") {
		return errors.New("域名必须以 https:// 开头")
	}
	admin := make([]byte, 24)
	if _, err = rand.Read(admin); err != nil {
		return err
	}
	adminPassword := base64.RawURLEncoding.EncodeToString(admin)
	adminPath := filepath.Join(dir, "admin-password")
	if err = writeOwnerOnly(adminPath, []byte(adminPassword+"\n")); err != nil {
		return fmt.Errorf("管理员密码文件无法写入（可能已存在）: %w", err)
	}
	env := fmt.Sprintf("XGIFT_ORIGIN=%s\nXGIFT_LISTEN=127.0.0.1:8787\nXGIFT_DATA_DIR=%s\nXGIFT_ADMIN_PASSWORD_FILE=%s\nXGIFT_PAYMENTS_ENABLED=true\n", origin, dir, adminPath)
	envPath := filepath.Join(dir, "site.env")
	if err = writeOwnerOnly(envPath, []byte(env)); err != nil {
		return fmt.Errorf("站点配置无法写入（可能已存在）: %w", err)
	}
	fmt.Printf("\n配置完成。\n站点配置：%s\n管理员密码文件：%s（管理员用户名 admin）\n", envPath, adminPath)
	fmt.Println("下一步：运行 xgift status 校验全部记录，然后启动 xgift-web。")
	return nil
}
