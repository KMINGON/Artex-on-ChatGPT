import type { ChatGPTAccount } from "@/lib/types";

// A registration ID is public metadata; equal emails must remain distinguishable.
export function chatGPTAccountLabel(account: Pick<ChatGPTAccount, "id" | "email">): string {
  return account.email ? `${account.email} · ${account.id}` : account.id;
}
