import { Provider } from '../../../../framework/http.ts';
import { routes } from '../../../routes/web.ts';
export class RouteServiceProvider extends Provider {
    register() {
    }
    boot() {
        this.app
            .router()
            .group({ middleware: 'web' }, routes);
    }
}
//# sourceMappingURL=RouteServiceProvider.js.map