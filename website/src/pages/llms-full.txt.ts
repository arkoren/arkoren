import type { APIRoute } from "astro";
import { rootFull } from "../lib/docs/llms";

export const GET: APIRoute = async () =>
  new Response(await rootFull(), { headers: { "Content-Type": "text/plain; charset=utf-8" } });
