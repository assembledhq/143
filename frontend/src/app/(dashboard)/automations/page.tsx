import { redirect } from "next/navigation";

export default function AutomationsPage() {
  redirect("/projects?status=scheduled");
}
