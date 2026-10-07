import Link from "next/link"
import { GiftIcon } from "lucide-react"

import { Announcement } from "@/components/announcement"
import { ThemeToggle } from "@/components/theme-toggle"
import { buttonVariants } from "@/components/ui/button"
import { cn } from "@/lib/utils"

export default function HomePage() {
  return (
    <div className="relative isolate flex min-h-svh flex-col">
      <Backdrop />
      <Announcement />

      <header className="relative z-10 flex items-center justify-between px-6 py-4 sm:px-10">
        <Link href="/" className="flex items-center gap-2.5 font-semibold tracking-tight">
          <span className="flex size-9 items-center justify-center rounded-xl bg-primary text-primary-foreground">
            <GiftIcon className="size-4.5" />
          </span>
          XGift
        </Link>
        <ThemeToggle />
      </header>

      {/* The page is one sentence and one button, so the type is the design: it
          sits on the optical centre of the viewport with nothing competing. */}
      <main className="relative z-10 flex flex-1 items-center px-6 py-16 sm:px-10">
        <div className="mx-auto w-full max-w-6xl">
          <h1 className="max-w-4xl text-4xl font-extrabold tracking-[-0.035em] text-balance sm:text-6xl sm:leading-[1.08] lg:text-7xl">
            用兑换码开通{" "}
            <span className="bg-gradient-to-r from-blue-500 via-sky-400 to-indigo-500 bg-clip-text text-transparent dark:from-sky-300 dark:via-blue-300 dark:to-indigo-300">
              X Premium
            </span>
          </h1>

          <div className="mt-8 sm:mt-12">
            <Link
              href="/redeem/"
              className={cn(
                buttonVariants({ size: "lg" }),
                "gap-2 px-8 text-base font-semibold shadow-lg shadow-primary/25",
              )}
            >
              <GiftIcon className="size-5" />
              去兑换会员
            </Link>
          </div>
        </div>
      </main>
    </div>
  )
}

// The backdrop is a single soft glow, drawn in CSS. It replaced a looping webm
// and its poster frame: those cost 4 MB in the served page and another 4 MB
// inside the Go binary, for a wash of colour the compositor now produces from
// two numbers. A radial gradient rather than a blurred shape, because blur
// filters repaint an area this large on the CPU.
//
// The hue tracks the gradient in the heading, and the two alphas are separate
// literals because the glow reads as ambient light on white but has to be
// lifted to stay visible on near-black.
function Backdrop() {
  return (
    <div aria-hidden className="pointer-events-none fixed inset-0 -z-10 overflow-hidden bg-background">
      <div className="absolute left-1/2 top-[-60%] aspect-square w-[150vw] max-w-[110rem] -translate-x-1/2 rounded-full bg-[radial-gradient(closest-side,oklch(0.55_0.2_264/0.18),transparent)] dark:bg-[radial-gradient(closest-side,oklch(0.72_0.16_264/0.22),transparent)]" />
    </div>
  )
}
