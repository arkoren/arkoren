const { Buffer } = Deno;
import { ServerRequest } from "https://deno.land/std@v0.11.0/http/server.ts";
import { BufReader, UnexpectedEOFError } from "https://deno.land/std@v0.11.0/io/bufio.ts";
import { TextProtoReader } from "https://deno.land/std@v0.11.0/textproto/mod.ts";
import { assertEquals } from "https://deno.land/std@v0.11.0/testing/asserts.ts";
// const s = serve("0.0.0.0:8000");

async function main() {
    const req = new ServerRequest();
    req.headers = new Headers();
    req.headers.set("content-length", "0");
    const buf = new Buffer;
    req.r = new BufReader(buf);
    const body = new TextDecoder().decode(await req.body());
    assertEquals(body, '');
}

async function getBody(stream: AsyncIterableIterator<Uint8Array>) {
    return collectUint8Arrays(stream)
}

async function *streamBody(req: ServerRequest): AsyncIterableIterator<Uint8Array> {
    console.log('Gen')
    if (req.headers.has("content-length")) {
        console.log('Has content-length')
      const len = +req.headers.get("content-length")!;
      if (Number.isNaN(len) || len == 0) {
        return new Uint8Array(0);
      }
      let buf = new Uint8Array(1024);
      console.log('Reading: ', len)
      let rr = await req.r.read(buf);
      console.log('Not reading')
      let nread = rr === Deno.EOF ? 0 : rr;
      let nreadTotal = nread;
      while (rr !== Deno.EOF && nreadTotal < len) {
        yield buf.subarray(0, nread);
        buf = new Uint8Array(1024);
        rr = await req.r.read(buf);
        nread = rr === Deno.EOF ? 0 : rr;
        nreadTotal += nread;
      }
      yield buf.subarray(0, nread);
    } else {
      if (req.headers.has("transfer-encoding")) {
        const transferEncodings = req.headers
          .get("transfer-encoding")!
          .split(",")
          .map((e): string => e.trim().toLowerCase());
        if (transferEncodings.includes("chunked")) {
          // Based on https://tools.ietf.org/html/rfc2616#section-19.4.6
          const tp = new TextProtoReader(req.r);
          let line = await tp.readLine();
          if (line === Deno.EOF) throw new UnexpectedEOFError();
          // TODO: handle chunk extension
          let [chunkSizeString] = line.split(";");
          let chunkSize = parseInt(chunkSizeString, 16);
          if (Number.isNaN(chunkSize) || chunkSize < 0) {
            throw new Error("Invalid chunk size");
          }
          while (chunkSize > 0) {
            const data = new Uint8Array(chunkSize);
            if ((await req.r.readFull(data)) === Deno.EOF) {
              throw new UnexpectedEOFError();
            }
            yield data;
            await req.r.readLine(); // Consume \r\n
            line = await tp.readLine();
            if (line === Deno.EOF) throw new UnexpectedEOFError();
            chunkSize = parseInt(line, 16);
          }
          const entityHeaders = await tp.readMIMEHeader();
          if (entityHeaders !== Deno.EOF) {
            for (let [k, v] of entityHeaders) {
                req.headers.set(k, v);
            }
          }
          /* Pseudo code from https://tools.ietf.org/html/rfc2616#section-19.4.6
          length := 0
          read chunk-size, chunk-extension (if any) and CRLF
          while (chunk-size > 0) {
            read chunk-data and CRLF
            append chunk-data to entity-body
            length := length + chunk-size
            read chunk-size and CRLF
          }
          read entity-header
          while (entity-header not empty) {
            append entity-header to existing header fields
            read entity-header
          }
          Content-Length := length
          Remove "chunked" from Transfer-Encoding
          */
          return; // Must return here to avoid fall through
        }
        // TODO: handle other transfer-encoding types
      }
      // Otherwise...
      yield new Uint8Array(0);
    }
}

async function collectUint8Arrays(
    it: AsyncIterable<Uint8Array>
  ): Promise<Uint8Array> {
    const chunks = [];
    let length = 0;
    console.log('S')
    for await (const chunk of it) {
        console.log('!')
      chunks.push(chunk);
      length += chunk.length;
    }
    console.log('F')
    if (chunks.length === 1) {
      // No need to copy.
      return chunks[0];
    }
    const collected = new Uint8Array(length);
    let offset = 0;
    for (let chunk of chunks) {
      collected.set(chunk, offset);
      offset += chunk.length;
    }
    return collected;
  }

main();
