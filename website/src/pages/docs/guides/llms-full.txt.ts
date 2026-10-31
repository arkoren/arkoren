import type { APIRoute } from "astro";
import { guidesFull } from "../../../lib/docs/llms";

export const GET: APIRoute = async () =>
  new Response(await guidesFull(), { headers: { "Content-Type": "text/plain; charset=utf-8" } });
