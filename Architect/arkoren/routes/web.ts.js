import { Log } from '../app/Http/Middleware/Log.ts';
import { MainController } from '../app/Http/Controllers/MainController.ts';
export const routes = (route) => {
    route.get('/', MainController.index);
    route.get('/another', MainController.another, { middleware: Log });
    route.get('/book/:name/:page', MainController.book);
    route.group({ middleware: Log }, route => {
        route.get('/test', MainController.index);
    });
};
//# sourceMappingURL=web.js.map