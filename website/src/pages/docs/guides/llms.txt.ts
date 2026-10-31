import type { APIRoute } from "astro";
import { guidesIndex } from "../../../lib/docs/llms";

export const GET: APIRoute = async () =>
  new Response(await guidesIndex(), { headers: { "Content-Type": "text/plain; charset=utf-8" } });
