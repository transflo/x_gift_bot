"use client"

import * as React from "react"
import Link from "next/link"
import {
  BarChart3Icon,
  GiftIcon,
  KeyRoundIcon,
  Link2Icon,
  Loader2Icon,
  LogOutIcon,
  ScrollTextIcon,
  ServerIcon,
  SettingsIcon,
  TicketIcon,
} from "lucide-react"

import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ThemeToggle } from "@/components/theme-toggle"
import { adminApi } from "@/lib/api"

import { AccountsPanel } from "./accounts-panel"
import { CodesPanel } from "./codes-panel"
import { LinkPanel } from "./link-panel"
import { LogsPanel } from "./logs-panel"
import { InlineError } from "./shared"
import { OverviewPanel } from "./overview-panel"
import { SettingsPanel } from "./settings-panel"

const STORAGE_KEY = "xgift-admin-password"

export default function AdminPage() {
  const [password, setPassword] = React.useState<string | null>(null)
  const [draft, setDraft] = React.useState("")
  const [checking, setChecking] = React.useState(true)
  const [error, setError] = React.useState("")
  const [tab, setTab] = React.useState("overview")

  const verify = React.useCallback(async (candidate: string): Promise<boolean> => {
    const result = await adminApi<unknown>(candidate, "/api/admin/stats")
    return result.ok
  }, [])

  React.useEffect(() => {
    const stored = sessionStorage.getItem(STORAGE_KEY)
    if (!stored) {
      setChecking(false)
      return
    }
    void verify(stored).then((ok) => {
      if (ok) setPassword(stored)
      else sessionStorage.removeItem(STORAGE_KEY)
      setChecking(false)
    })
  }, [verify])

  async function login(event: React.FormEvent) {
    event.preventDefault()
    setChecking(true)
    setError("")
    const ok = await verify(draft)
    setChecking(false)
    if (!ok) {
      setError("密码不正确。")
      return
    }
    sessionStorage.setItem(STORAGE_KEY, draft)
    setPassword(draft)
    setDraft("")
  }

  if (checking && password === null) {
    return (
      <div className="flex min-h-svh items-center justify-center">
        <Loader2Icon className="size-5 animate-spin text-muted-foreground" />
      </div>
    )
  }

  if (password === null) {
    return (
      <div className="flex min-h-svh items-center justify-center px-4">
        <Card className="w-full max-w-sm">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <KeyRoundIcon className="size-4" />
              管理员登录
            </CardTitle>
            <CardDescription>输入站点管理员密码（部署时在 .env 里设置的 XGIFT_ADMIN_PASSWORD）。</CardDescription>
          </CardHeader>
          <CardContent>
            <form className="grid gap-4" onSubmit={login}>
              <div className="grid gap-2">
                <Label htmlFor="admin-password">管理员密码</Label>
                <Input
                  id="admin-password"
                  type="password"
                  autoComplete="current-password"
                  value={draft}
                  onChange={(event) => setDraft(event.target.value)}
                />
              </div>
              <InlineError>{error}</InlineError>
              <Button type="submit" disabled={!draft || checking}>
                {checking ? <Loader2Icon className="animate-spin" /> : null}
                登录
              </Button>
            </form>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="min-h-svh bg-background">
      <header className="sticky top-0 z-20 border-b border-border/60 bg-background/85 backdrop-blur">
        <div className="mx-auto flex w-full max-w-7xl items-center justify-between gap-3 px-4 py-3 sm:px-6">
          <div className="flex items-center gap-2">
            <span className="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
              <GiftIcon className="size-4" />
            </span>
            <div>
              <p className="text-sm font-semibold leading-tight">XGift 管理后台</p>
              <p className="text-xs text-muted-foreground">兑换码 · 账号池 · 付款链接</p>
            </div>
          </div>
          <div className="flex items-center gap-1">
            <Link href="/" className="text-sm text-muted-foreground underline-offset-4 hover:underline">
              返回兑换页
            </Link>
            <ThemeToggle />
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                sessionStorage.removeItem(STORAGE_KEY)
                setPassword(null)
              }}
            >
              <LogOutIcon />
              退出
            </Button>
          </div>
        </div>
      </header>

      <main className="mx-auto w-full max-w-7xl px-4 py-6 sm:px-6">
        <Tabs value={tab} onValueChange={(value) => setTab(value as string)}>
          <TabsList className="mb-4 h-auto flex-wrap">
            <TabsTrigger value="overview">
              <BarChart3Icon />
              概览
            </TabsTrigger>
            <TabsTrigger value="codes">
              <TicketIcon />
              兑换码
            </TabsTrigger>
            <TabsTrigger value="accounts">
              <ServerIcon />
              账号池
            </TabsTrigger>
            <TabsTrigger value="links">
              <Link2Icon />
              生成链接
            </TabsTrigger>
            <TabsTrigger value="settings">
              <SettingsIcon />
              设置
            </TabsTrigger>
            <TabsTrigger value="logs">
              <ScrollTextIcon />
              日志
            </TabsTrigger>
          </TabsList>
          <TabsContent value="overview">
            <OverviewPanel password={password} />
          </TabsContent>
          <TabsContent value="codes">
            <CodesPanel password={password} />
          </TabsContent>
          <TabsContent value="accounts">
            <AccountsPanel password={password} />
          </TabsContent>
          <TabsContent value="links">
            <LinkPanel password={password} />
          </TabsContent>
          <TabsContent value="settings">
            <SettingsPanel password={password} />
          </TabsContent>
          <TabsContent value="logs">
            <LogsPanel password={password} />
          </TabsContent>
        </Tabs>
      </main>
    </div>
  )
}
