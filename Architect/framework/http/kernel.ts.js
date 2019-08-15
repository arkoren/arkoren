import { NotFound } from './errors.ts';
import { Router } from './router.ts';
export class Kernel {
    constructor() {
        this.middleware_instances = [];
        this.middleware = [];
        this.middleware_groups = {};
        this.routes = [];
        this.router = new Router(this);
    }
    handle(request) {
        const [route, parameters] = this.router.matchRoute(request);
        if (route) {
            return route.handle(request, parameters, this.middleware_instances);
        }
        throw new NotFound;
    }
    bootstrap() {
        for (const register of this.routes) {
            register(this.router);
        }
        this.middleware_instances = this.middleware.map(middleware => new middleware);
    }
    add_routes(routes) {
        this.routes.push(routes);
    }
    middlewares_of(group) {
        if (this.middleware_groups.hasOwnProperty(group)) {
            return this.middleware_groups[group];
        }
        return [];
    }
}
//# sourceMappingURL=kernel.js.map