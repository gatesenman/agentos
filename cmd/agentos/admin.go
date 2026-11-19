package main

// Owned by module M7. Offline admin utilities operating directly on the DB
// (<data>/server/agentos.db). Safe to run while the server is up: the server
// reads secrets and sessions from SQLite on every request.
//
//	agentos admin reset-password    [--data DIR] [NEWPASSWORD]   (random if omitted; signs out all sessions)
//	agentos admin rotate-node-token [--data DIR]
import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/agentos/agentos/internal/auth"
	"github.com/agentos/agentos/internal/store"
)

const adminUsage = `用法 / usage:
  agentos admin reset-password    [--data DIR] [NEWPASSWORD]   重置管理员密码 (省略则随机生成), 并注销所有会话
  agentos admin rotate-node-token [--data DIR]                 轮换节点令牌 (已连接节点需使用新令牌重连)
`

func runAdmin(args []string) error {
	return adminMain(args, os.Stdout, DefaultDataDir())
}

func adminMain(args []string, out io.Writer, defData string) error {
	if len(args) < 1 {
		fmt.Fprint(out, adminUsage)
		return errors.New("missing admin subcommand")
	}
	cmd := args[0]
	fs := flag.NewFlagSet("admin "+cmd, flag.ContinueOnError)
	fs.SetOutput(out)
	dataDir := fs.String("data", defData, "data directory")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	open := func() (store.Store, *auth.Manager, error) {
		db := filepath.Join(*dataDir, "server", "agentos.db")
		if _, err := os.Stat(db); err != nil {
			return nil, nil, fmt.Errorf("数据库不存在 / database not found: %s (use --data)", db)
		}
		st, err := store.OpenSQLite(db)
		if err != nil {
			return nil, nil, err
		}
		am, _, err := auth.New(context.Background(), st, "")
		if err != nil {
			st.Close()
			return nil, nil, err
		}
		return st, am, nil
	}
	ctx := context.Background()
	switch cmd {
	case "reset-password":
		if fs.NArg() > 1 {
			return errors.New("usage: agentos admin reset-password [--data DIR] [NEWPASSWORD]")
		}
		pw, generated := fs.Arg(0), false
		if pw == "" {
			pw, generated = auth.RandomToken(6), true
		}
		st, am, err := open()
		if err != nil {
			return err
		}
		defer st.Close()
		if err := am.SetPassword(ctx, pw); err != nil {
			return err
		}
		// Keep tray/login reveal file in sync with the live password.
		_ = writeInitialAdminPasswordFile(filepath.Join(*dataDir, "server"), pw)
		if err := am.RevokeOtherSessions(ctx, ""); err != nil {
			return err
		}
		if generated {
			fmt.Fprintf(out, "新管理员密码 / new admin password: %s\n", pw)
		} else {
			fmt.Fprintln(out, "管理员密码已重置 / admin password reset")
		}
		fmt.Fprintln(out, "所有会话已注销 / all sessions signed out")
		return nil
	case "rotate-node-token":
		if fs.NArg() != 0 {
			return errors.New("usage: agentos admin rotate-node-token [--data DIR]")
		}
		st, am, err := open()
		if err != nil {
			return err
		}
		defer st.Close()
		tok, err := am.RotateNodeToken(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "新节点令牌 / new node token: %s\n", tok)
		fmt.Fprintln(out, "已连接节点需使用新令牌重连 / connected nodes must reconnect with the new token")
		return nil
	case "-h", "--help", "help":
		fmt.Fprint(out, adminUsage)
		return nil
	default:
		fmt.Fprint(out, adminUsage)
		return fmt.Errorf("unknown admin subcommand %q", cmd)
	}
}
