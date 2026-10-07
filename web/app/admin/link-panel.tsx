"use client"

import * as React from "react"
import { ExternalLinkIcon, Loader2Icon, WandSparklesIcon } from "lucide-react"

import { Button, buttonVariants } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { adminApi, formatAmount } from "@/lib/api"
import { cn } from "@/lib/utils"

import { CopyButton, InlineError, PanelStatus, type ManualPlan } from "./shared"

type LinkResult = {
  username: string
  months: number
  amount: number
  currency: string
  status: string
  checkout_url?: string
  expires_at?: number
  message?: string
}

export function LinkPanel({ password }: { password: string }) {
  const [plans, setPlans] = React.useState<ManualPlan[]>([])
  const [username, setUsername] = React.useState("")
  const [months, setMonths] = React.useState("")
  const [busy, setBusy] = React.useState(false)
  const [error, setError] = React.useState("")
  const [notice, setNotice] = React.useState("")
  const [result, setResult] = React.useState<LinkResult | null>(null)

  React.useEffect(() => {
    void adminApi<{ plans: ManualPlan[] }>(password, "/api/admin/manual-link/plans").then(
      ({ ok, data }) => {
        if (!ok) return
        setPlans(data.plans ?? [])
        if (data.plans?.length && !months) setMonths(String(data.plans[0].months))
      },
    )
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [password])

  async function submit(event: React.FormEvent) {
    event.preventDefault()
    setBusy(true)
    setError("")
    setNotice("")
    setResult(null)
    const clean = username.trim().replace(/^@/, "").toLowerCase()
    const response = await adminApi<LinkResult>(password, "/api/admin/manual-link", {
      username: clean,
      months: Number(months),
    })
    setBusy(false)
    if (response.status === 429) {
      setError(response.message || "通道繁忙，请稍后重试。")
      return
    }
    if (!response.ok) {
      setError(response.message || "无法生成付款链接。")
      return
    }
    setResult(response.data)
    setNotice(response.data.status === "succeeded" ? "该订单已完成付款。" : "付款链接已生成。")
  }

  const cleanValid = /^[a-z0-9_]{1,15}$/.test(username.trim().replace(/^@/, "").toLowerCase())

  return (
    <div className="grid gap-4">
      <Card>
        <CardHeader>
          <CardTitle className="text-base">为客户生成付款链接</CardTitle>
          <CardDescription>
            不需要兑换码。系统会复用该账号已有的未付款链接；已有其他套餐的订单会拒绝创建。
          </CardDescription>
        </CardHeader>
        <CardContent>
          <form className="grid gap-4" onSubmit={submit}>
            <div className="grid gap-2">
              <Label htmlFor="manual-username">X 用户名</Label>
              <Input
                id="manual-username"
                value={username}
                autoComplete="off"
                spellCheck={false}
                placeholder="@username"
                onChange={(event) => setUsername(event.target.value)}
                aria-invalid={username.length > 0 && !cleanValid}
              />
            </div>
            <div className="grid gap-2">
              <Label>套餐</Label>
              <Select value={months} onValueChange={(value) => setMonths(value as string)}>
                <SelectTrigger className="w-full sm:w-64">
                  <SelectValue placeholder="选择套餐" />
                </SelectTrigger>
                <SelectContent>
                  {plans.map((plan) => (
                    <SelectItem key={plan.months} value={String(plan.months)}>
                      {plan.months} 个月 · {formatAmount(plan.amount, plan.currency)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div>
              <Button type="submit" disabled={busy || !cleanValid || !months}>
                {busy ? <Loader2Icon className="animate-spin" /> : <WandSparklesIcon />}
                生成链接
              </Button>
            </div>
          </form>
          <div className="mt-3 grid gap-2">
            <InlineError>{error}</InlineError>
            <PanelStatus>{notice}</PanelStatus>
          </div>
        </CardContent>
      </Card>

      {result ? (
        <Card>
          <CardHeader>
            <CardTitle className="text-base">
              @{result.username} · {result.months} 个月
            </CardTitle>
            <CardDescription>
              {formatAmount(result.amount, result.currency)} · {result.status}
            </CardDescription>
          </CardHeader>
          <CardContent className="grid gap-3">
            {result.checkout_url ? (
              <>
                <p className="break-all rounded-lg border border-border bg-muted/40 p-3 font-mono text-xs">
                  {result.checkout_url}
                </p>
                <div className="flex flex-wrap gap-2">
                  <a
                    className={cn(buttonVariants({ variant: "default" }))}
                    href={result.checkout_url}
                    target="_blank"
                    rel="noreferrer noopener"
                  >
                    <ExternalLinkIcon />
                    打开付款页
                  </a>
                  <CopyButton value={result.checkout_url} label="复制链接" />
                </div>
                {result.expires_at ? (
                  <p className="text-xs text-muted-foreground">
                    链接有效期至 {new Date(result.expires_at * 1000).toLocaleTimeString("zh-CN")}
                  </p>
                ) : null}
              </>
            ) : (
              <p className="text-sm text-muted-foreground">{result.message || "该订单无需再次付款。"}</p>
            )}
          </CardContent>
        </Card>
      ) : null}
    </div>
  )
}
