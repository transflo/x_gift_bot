"use client"

import * as React from "react"
import Link from "next/link"
import {
  AlertTriangleIcon,
  ArrowRightIcon,
  CheckCircle2Icon,
  ClockIcon,
  ExternalLinkIcon,
  GiftIcon,
  Loader2Icon,
  RefreshCwIcon,
  SearchIcon,
  ShieldCheckIcon,
  SparklesIcon,
} from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Button, buttonVariants } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Progress } from "@/components/ui/progress"
import { Separator } from "@/components/ui/separator"
import { Announcement } from "@/components/announcement"
import { ThemeToggle } from "@/components/theme-toggle"
import { Turnstile } from "@/components/turnstile"
import { api, type Redemption, type Security } from "@/lib/api"
import { cn } from "@/lib/utils"

const CODE_PATTERN = /^XG-[A-F0-9]{48}$/
const USERNAME_PATTERN = /^[a-z0-9_]{1,15}$/

function cleanCode(value: string) {
  return value.replace(/\s+/g, "").toUpperCase()
}

function cleanUsername(value: string) {
  return value.trim().replace(/^@/, "").toLowerCase()
}

function useCountdown(expiresAt?: number) {
  const [now, setNow] = React.useState(() => Date.now())
  React.useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])
  if (!expiresAt) return { seconds: null as number | null, expired: false }
  const seconds = Math.max(0, expiresAt - Math.floor(now / 1000))
  return { seconds, expired: seconds <= 0 }
}

function formatSeconds(seconds: number) {
  const minutes = Math.floor(seconds / 60)
  const rest = seconds % 60
  return `${minutes}:${rest.toString().padStart(2, "0")}`
}

function statusBadge(status?: string) {
  switch (status) {
    case "succeeded":
      return <Badge>已完成</Badge>
    case "processing":
      return <Badge variant="secondary">处理中</Badge>
    case "review":
      return <Badge variant="destructive">待管理员处理</Badge>
    case "revoked":
      return <Badge variant="outline">已停用</Badge>
    default:
      return <Badge variant="outline">未开始</Badge>
  }
}

type Step = { label: string; done: boolean; active: boolean }

function stepsFor(redemption: Redemption | null): Step[] {
  const progress = redemption?.progress ?? 0
  const ready = Boolean(redemption?.link_ready)
  const succeeded = redemption?.status === "succeeded"
  return [
    { label: "核对账号", done: progress >= 25 || ready || succeeded, active: progress < 25 && progress > 0 },
    { label: "生成付款链接", done: ready, active: progress >= 25 && !ready },
    { label: "等待付款", done: succeeded, active: ready && !succeeded },
    { label: "兑换完成", done: succeeded, active: false },
  ]
}

export default function RedeemPage() {
  const [code, setCode] = React.useState("")
  const [username, setUsername] = React.useState("")
  const [busy, setBusy] = React.useState<"redeem" | "status" | "regenerate" | null>(null)
  const [redemption, setRedemption] = React.useState<Redemption | null>(null)
  const [error, setError] = React.useState("")
  const [security, setSecurity] = React.useState<Security | null>(null)
  const [turnstileToken, setTurnstileToken] = React.useState("")

  const cleanCodeValue = cleanCode(code)
  const cleanUsernameValue = cleanUsername(username)
  const codeValid = CODE_PATTERN.test(cleanCodeValue)
  const usernameValid = USERNAME_PATTERN.test(cleanUsernameValue)
  const formValid = codeValid && usernameValid
  const current = React.useRef({ code: "", username: "" })
  const countdown = useCountdown(redemption?.expires_at)
  const expired = countdown.expired && Boolean(redemption?.expires_at)

  React.useEffect(() => {
    api<Security>("/api/security").then(({ ok, data }) => {
      if (ok) setSecurity(data)
    })
  }, [])

  const poll = React.useCallback(async () => {
    const { code, username } = current.current
    const result = await api<Redemption>("/api/status", { code, username })
    if (result.ok && result.data) {
      setRedemption((previous) => ({ ...previous, ...result.data }))
      if (result.status === 200) setError("")
    }
    return result.ok ? result.data : null
  }, [])

  React.useEffect(() => {
    const status = redemption?.status
    if (status !== "processing" || !current.current.code) return
    const timer = window.setInterval(() => {
      void poll()
    }, 3000)
    return () => window.clearInterval(timer)
  }, [redemption?.status, poll])

  async function submit(kind: "redeem" | "status") {
    if (!formValid || busy) return
    setBusy(kind)
    setError("")
    current.current = { code: cleanCodeValue, username: cleanUsernameValue }
    const headers = turnstileToken ? { "X-Turnstile-Token": turnstileToken } : undefined
    const result =
      kind === "redeem"
        ? await api<Redemption>("/api/redeem", { code: cleanCodeValue, username: cleanUsernameValue }, { headers })
        : await api<Redemption>("/api/status", { code: cleanCodeValue, username: cleanUsernameValue })
    setBusy(null)
    if (!result.ok) {
      if (result.status === 503) {
        setError(result.message || "充值暂时暂停，请稍后重试。")
      } else {
        setError(result.message || "操作失败，请稍后重试。")
      }
      return
    }
    setRedemption(result.data)
    if (kind === "redeem") setTurnstileToken("")
  }

  async function regenerate() {
    if (!formValid || busy || !redemption?.can_regenerate) return
    setBusy("regenerate")
    setError("")
    const headers = turnstileToken ? { "X-Turnstile-Token": turnstileToken } : undefined
    const result = await api<Redemption>(
      "/api/redeem/regenerate",
      { code: cleanCodeValue, username: cleanUsernameValue },
      { headers },
    )
    setBusy(null)
    if (!result.ok) {
      setError(result.message || "暂时无法重新生成，请稍后重试。")
      return
    }
    setRedemption((previous) => ({ ...previous, ...result.data, link_ready: false, checkout_url: "" }))
    setTurnstileToken("")
  }

  const steps = stepsFor(redemption)
  const ready = Boolean(redemption?.link_ready && redemption?.checkout_url && !expired)
  const succeeded = redemption?.status === "succeeded"
  const showRegenerate =
    Boolean(redemption?.can_regenerate) &&
    Boolean(redemption?.checkout_url) &&
    !ready &&
    !succeeded

  return (
    <div className="flex min-h-svh flex-col bg-background">
      <Announcement />

      <header className="sticky top-0 z-20 border-b border-border/60 bg-background/85 backdrop-blur">
        <div className="mx-auto flex w-full max-w-5xl items-center justify-between gap-3 px-4 py-3 sm:px-6">
          <Link href="/" className="flex items-center gap-2 font-semibold tracking-tight">
            <span className="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground">
              <GiftIcon className="size-4" />
            </span>
            XGift
          </Link>
          <div className="flex items-center gap-1">
            <ThemeToggle />
          </div>
        </div>
      </header>

      <main className="mx-auto w-full max-w-5xl flex-1 px-4 py-8 sm:px-6 sm:py-12">
        <section className="mx-auto max-w-2xl text-center">
          <Badge variant="outline" className="mb-4 gap-1">
            <SparklesIcon className="size-3" />
            X Premium 礼品兑换
          </Badge>
          <h1 className="text-3xl font-semibold tracking-tight text-balance sm:text-4xl">
            用兑换码，为你的 X 账号开通 Premium
          </h1>
          <p className="mt-4 text-base leading-relaxed text-muted-foreground">
            输入兑换码与 X 用户名，系统会立即为你创建专属的 Stripe 付款链接。无需提供密码，
            付款完成后本页面会自动更新。
          </p>
        </section>

        <div className="mx-auto mt-8 grid w-full max-w-3xl gap-6">
          <Card>
            <CardHeader>
              <CardTitle>开始兑换</CardTitle>
              <CardDescription>请确认兑换码与用户名填写正确，用户名不是显示名称。</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4">
              <form
                className="grid gap-4"
                onSubmit={(event) => {
                  event.preventDefault()
                  void submit("redeem")
                }}
              >
                <div className="grid gap-2">
                  <Label htmlFor="code">兑换码</Label>
                  <Input
                    id="code"
                    name="code"
                    value={code}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="XG-XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
                    className="font-mono text-xs sm:text-sm"
                    onChange={(event) => setCode(event.target.value)}
                    aria-invalid={code.length > 0 && !codeValid}
                    aria-describedby="code-hint"
                  />
                  <p id="code-hint" className="text-xs text-muted-foreground">
                    以 XG- 开头，共 51 个字符。粘贴时会自动去除空格。
                  </p>
                </div>
                <div className="grid gap-2">
                  <Label htmlFor="username">X 用户名</Label>
                  <Input
                    id="username"
                    name="username"
                    value={username}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder="@username"
                    onChange={(event) => setUsername(event.target.value)}
                    aria-invalid={username.length > 0 && !usernameValid}
                  />
                  <p className="text-xs text-muted-foreground">不带 @ 也可以，仅支持字母、数字和下划线。</p>
                </div>
                {security?.turnstile_enabled && security.turnstile_site_key ? (
                  <Turnstile
                    siteKey={security.turnstile_site_key}
                    action="redeem"
                    onToken={setTurnstileToken}
                  />
                ) : null}
                <div className="flex flex-col gap-2 sm:flex-row">
                  <Button type="submit" disabled={!formValid || busy !== null} className="sm:flex-1">
                    {busy === "redeem" ? (
                      <Loader2Icon className="animate-spin" />
                    ) : (
                      <ArrowRightIcon />
                    )}
                    {redemption?.status === "processing" && !redemption.link_ready
                      ? "继续生成链接"
                      : "开始兑换"}
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    disabled={!formValid || busy !== null}
                    onClick={() => void submit("status")}
                    className="sm:flex-1"
                  >
                    {busy === "status" ? <Loader2Icon className="animate-spin" /> : <SearchIcon />}
                    查询进度
                  </Button>
                </div>
              </form>
            </CardContent>
          </Card>

          {error ? (
            <Card className="border-destructive/40">
              <CardContent className="flex items-start gap-3 py-4 text-sm">
                <AlertTriangleIcon className="mt-0.5 size-4 shrink-0 text-destructive" />
                <p>{error}</p>
              </CardContent>
            </Card>
          ) : null}

          {redemption ? (
            <Card>
              <CardHeader className="gap-3">
                <div className="flex flex-wrap items-center justify-between gap-2">
                  <CardTitle>
                    {succeeded
                      ? "兑换完成"
                      : ready
                        ? "付款链接已就绪"
                        : redemption.status === "review"
                          ? "需要管理员核实"
                          : "正在处理"}
                  </CardTitle>
                  {statusBadge(redemption.status)}
                </div>
                <CardDescription className="text-sm text-foreground/80">
                  {redemption.message || "请稍候，系统正在处理你的兑换。"}
                </CardDescription>
              </CardHeader>
              <CardContent className="grid gap-5">
                <div>
                  <Progress value={succeeded ? 100 : (redemption.progress ?? 0)} />
                  <div className="mt-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
                    {steps.map((step) => (
                      <div key={step.label} className="flex items-center gap-2 text-xs">
                        {step.done ? (
                          <CheckCircle2Icon className="size-3.5 text-primary" />
                        ) : step.active ? (
                          <Loader2Icon className="size-3.5 animate-spin text-muted-foreground" />
                        ) : (
                          <ClockIcon className="size-3.5 text-muted-foreground" />
                        )}
                        <span
                          className={cn(
                            "truncate",
                            step.done ? "text-foreground" : "text-muted-foreground",
                          )}
                        >
                          {step.label}
                        </span>
                      </div>
                    ))}
                  </div>
                </div>

                {redemption.months ? (
                  <div className="flex flex-wrap items-center gap-2 text-sm text-muted-foreground">
                    <span>套餐：{redemption.months} 个月 Premium</span>
                    {redemption.username ? <Separator orientation="vertical" className="h-3.5" /> : null}
                    {redemption.username ? <span>接收账号：@{redemption.username}</span> : null}
                  </div>
                ) : null}

                {redemption.checkout_url && !succeeded ? (
                  <div className="grid gap-3 rounded-xl border border-border bg-muted/40 p-4">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <p className="text-sm font-medium">请在 Stripe 页面完成付款</p>
                      {redemption.expires_at && !succeeded ? (
                        <span
                          className={cn(
                            "font-mono text-xs",
                            expired ? "text-destructive" : "text-muted-foreground",
                          )}
                        >
                          {expired
                            ? "链接已过期"
                            : `有效期剩余 ${formatSeconds(countdown.seconds ?? 0)}`}
                        </span>
                      ) : null}
                    </div>
                    <p className="text-xs leading-relaxed text-muted-foreground">
                      付款即完成兑换。请勿重复支付；若链接过期，每个兑换码可以重新生成一次。
                    </p>
                    <div className="flex flex-col gap-2 sm:flex-row">
                      <a
                        href={redemption.checkout_url}
                        target="_blank"
                        rel="noreferrer noopener"
                        aria-disabled={expired}
                        className={cn(
                          buttonVariants({ variant: "default" }),
                          "sm:flex-1",
                          expired && "pointer-events-none opacity-50",
                        )}
                      >
                        <ExternalLinkIcon />
                        打开 Stripe 付款页
                      </a>
                      {showRegenerate ? (
                        <Button
                          type="button"
                          variant="outline"
                          onClick={() => void regenerate()}
                          disabled={busy !== null}
                        >
                          {busy === "regenerate" ? (
                            <Loader2Icon className="animate-spin" />
                          ) : (
                            <RefreshCwIcon />
                          )}
                          重新生成链接
                        </Button>
                      ) : null}
                    </div>
                  </div>
                ) : null}

                {succeeded ? (
                  <div className="flex items-start gap-3 rounded-xl border border-primary/30 bg-primary/5 p-4 text-sm">
                    <CheckCircle2Icon className="mt-0.5 size-4 shrink-0 text-primary" />
                    <p>已为 @{redemption.username} 完成 Premium 赠送。若 X 未立即刷新，请重新打开 X 查看。</p>
                  </div>
                ) : null}
              </CardContent>
            </Card>
          ) : null}
        </div>

        <section className="mx-auto mt-12 grid w-full max-w-3xl gap-4 sm:grid-cols-3">
          {[
            {
              icon: ShieldCheckIcon,
              title: "无需密码",
              text: "全程不需要 X 密码，仅使用兑换码与公开用户名。",
            },
            {
              icon: ExternalLinkIcon,
              title: "直达 Stripe",
              text: "付款在 Stripe 官方页面完成，金额与收款方可在付款前核对。",
            },
            {
              icon: ClockIcon,
              title: "进度可查",
              text: "保存本页面或随时输入兑换码查询最新进度。",
            },
          ].map((feature) => (
            <div key={feature.title} className="flex gap-3 rounded-xl border border-border p-4">
              <feature.icon className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
              <div>
                <p className="text-sm font-medium">{feature.title}</p>
                <p className="mt-1 text-xs leading-relaxed text-muted-foreground">{feature.text}</p>
              </div>
            </div>
          ))}
        </section>
      </main>

      <footer className="border-t border-border/60 py-6">
        <div className="mx-auto flex w-full max-w-5xl flex-col gap-2 px-4 text-xs text-muted-foreground sm:flex-row sm:items-center sm:justify-between sm:px-6">
          <p>XGift · 兑换码仅在有效期内使用，请勿泄露。</p>
          <p>付款完成后由 X 官方完成 Premium 赠送。</p>
        </div>
      </footer>
    </div>
  )
}
