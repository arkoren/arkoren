import { response } from './response.ts';
import { Method } from './request.ts';
class Parameter {
    constructor(name) {
        this.name = name;
    }
}
export class Route {
    constructor(method, path, handler, options) {
        this.segments = [];
        this.method = method;
        this.path = path;
        this.buildSegments();
        this.handler = handler;
        this.options = Route.mergeOptions(options[0], options[1]);
    }
    static segmentate(path) {
        return path.split('/').slice(1);
    }
    static optionsFrom({ middleware }, http_kernel) {
        let middlewares = [];
        if (middleware) {
            if (Array.isArray(middleware)) {
                middlewares = middleware.map(m => new m);
            }
            else if (typeof middleware === 'string') {
                middlewares = http_kernel.middlewares_of(middleware).map(m => new m);
            }
            else {
                middlewares = [new middleware];
            }
        }
        return {
            middlewares: middlewares
        };
    }
    static mergeOptions(first, second) {
        return {
            middlewares: [...first.middlewares, ...second.middlewares]
        };
    }
    buildSegments() {
        this.segments = Route.segmentate(this.path).map(segment => (typeof segment === 'string' && segment[0] == ':')
            ? new Parameter(segment.substr(1))
            : segment);
    }
    match(request) {
        const segments = Route.segmentate(request.path());
        if (segments.length !== this.segments.length || this.method !== request.method()) {
            return [false, {}];
        }
        const parameters = {};
        for (let i = 0; i < segments.length; ++i) {
            const current = this.segments[i];
            if (typeof current === 'string') {
                if (current !== segments[i]) {
                    return [false, {}];
                }
            }
            else {
                parameters[current.name] = segments[i];
            }
        }
        return [true, parameters];
    }
    handle(request, params, globals = []) {
        const middlewares = [...globals, ...this.options.middlewares];
        const full_request = { request, params };
        for (const middleware of middlewares) {
            const possible_response = middleware.before(full_request);
            if (possible_response) {
                return response(possible_response);
            }
        }
        const res = this.handler(full_request);
        for (const middleware of middlewares) {
            const possible_response = middleware.after(full_request, res);
            if (possible_response) {
                return response(possible_response);
            }
        }
        return response(res);
    }
}
export class Router {
    constructor(http_kernel, options = {}) {
        this.routes = [];
        this.http_kernel = http_kernel;
        this.options = options;
    }
    addRoute(method, path, handler, options) {
        const route = new Route(method, path, handler, [
            Route.optionsFrom(this.options, this.http_kernel),
            Route.optionsFrom(options, this.http_kernel)
        ]);
        this.routes.push(route);
        return route;
    }
    matchRoute(request) {
        for (const route of this.routes) {
            const [match, params] = route.match(request);
            if (match) {
                return [route, params];
            }
        }
        return [null, {}];
    }
    get(path, handler, options = {}) {
        return this.addRoute(Method.Get, path, handler, options);
    }
    post(path, handler, options = {}) {
        return this.addRoute(Method.Post, path, handler, options);
    }
    group(options, callback) {
        const router = new Router(this.http_kernel, { ...this.options, ...options });
        callback(router);
        this.routes = [...this.routes, ...router.routes];
    }
}
//# sourceMappingURL=router.js.map