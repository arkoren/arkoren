import type { APIRoute } from "astro";
import { rootIndex } from "../lib/docs/llms";

export const GET: APIRoute = async () =>
  new Response(await rootIndex(), { headers: { "Content-Type": "text/plain; charset=utf-8" } });
