"use client"

import * as React from "react"
import { EyeIcon, EyeOffIcon, KeyRoundIcon, Loader2Icon } from "lucide-react"

import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { adminApi } from "@/lib/api"

import { CopyButton, InlineError } from "./shared"

type VaultResponse = { path: string; password: string }

// The vault password is fetched only when the operator asks for it, so it does
// not sit in the browser until they actually want to back it up.
export function VaultCard({ password }: { password: string }) {
  const [data, setData] = React.useState<VaultResponse | null>(null)
  const [revealed, setRevealed] = React.useState(false)
  const [loading, setLoading] = React.useState(false)
  const [error, setError] = React.useState("")

  const load = React.useCallback(async () => {
    setLoading(true)
    setError("")
    const result = await adminApi<VaultResponse>(password, "/api/admin/vault")
    setLoading(false)
    if (!result.ok) {
      setError(result.message || "无法读取保管库密码。")
      return
    }
    setData(result.data)
  }, [password])

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">保管库备份</CardTitle>
        <CardDescription>
          保管库密码用于加密全部敏感记录，请连同数据卷一起备份。密码一旦丢失，记录无法恢复。
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <InlineError>{error}</InlineError>
        {data ? (
          <>
            <p className="font-mono text-xs break-all text-muted-foreground">{data.path}</p>
            <div className="flex flex-wrap items-center gap-2">
              <code className="min-w-0 flex-1 rounded-md border border-border bg-muted/40 px-3 py-2 font-mono text-xs break-all">
                {revealed ? data.password : "•".repeat(32)}
              </code>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => setRevealed((value) => !value)}
              >
                {revealed ? <EyeOffIcon /> : <EyeIcon />}
                {revealed ? "隐藏" : "显示"}
              </Button>
              <CopyButton value={data.password} label="复制密码" />
            </div>
          </>
        ) : (
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="justify-self-start"
            onClick={() => void load()}
            disabled={loading}
          >
            {loading ? <Loader2Icon className="animate-spin" /> : <KeyRoundIcon />}
            显示密码
          </Button>
        )}
      </CardContent>
    </Card>
  )
}
