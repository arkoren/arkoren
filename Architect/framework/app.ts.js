import { serve } from 'https://deno.land/std@v0.12.0/http/server.ts';
import { Status, NotFound, response, HTTPRequest, } from './http.ts';
export class App {
    constructor({ url, host, port, http_kernel, providers }) {
        this.provider_instances = [];
        this.services = {};
        this.url = url;
        this.host = host;
        this.port = port;
        this.http_kernel = new http_kernel;
        this.providers = providers;
    }
    respond(req, res) {
        req.respond({
            status: res.status(),
            headers: res.getHeaders(),
            body: res.content()
        });
    }
    handleResponse(req, res) {
        this.respond(req, response(res));
    }
    async handleRequest(req) {
        const hasBody = req.headers.has('Content-Length') && req.headers.get('Content-Length') !== '0';
        const request = new HTTPRequest(req.method, new URL(req.url, this.url), hasBody ? await req.body() : new Uint8Array(), req.headers, parseFloat(`${req.protoMajor}.${req.protoMinor}`));
        try {
            const response = this.http_kernel.handle(request);
            this.handleResponse(req, response);
        }
        catch (error) {
            if (error instanceof NotFound) {
                this.handleResponse(req, response(error.message, Status.NotFound));
            }
            else {
                this.handleResponse(req, response(error.message, Status.InternalServerError));
            }
        }
    }
    router() {
        return this.http_kernel.router;
    }
    async start() {
        const address = `${this.host}:${this.port}`;
        console.log('Initializing service providers...');
        this.provider_instances = this.providers.map(provider => new provider(this));
        console.log('Registering services...');
        for (const provider of this.provider_instances) {
            provider.register();
        }
        console.log('Booting services...');
        for (const provider of this.provider_instances) {
            provider.boot();
        }
        console.log('Bootstrapping HTTP Kernel...');
        this.http_kernel.bootstrap();
        console.log(`Starting application on: ${address}`);
        for await (const req of serve(address)) {
            this.handleRequest(req);
        }
    }
}
//# sourceMappingURL=app.js.map