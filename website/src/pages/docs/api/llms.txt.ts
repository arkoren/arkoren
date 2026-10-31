import type { APIRoute } from "astro";
import { apiIndex } from "../../../lib/docs/llms";

export const GET: APIRoute = async () =>
  new Response(await apiIndex(), { headers: { "Content-Type": "text/plain; charset=utf-8" } });
