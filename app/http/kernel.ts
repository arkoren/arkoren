import { IsMiddleware } from '../../../arkoren/http.ts'
import TrimStrings from './middleware/trim_strings.ts'

/**
 * HTTP Kernel class.
 *
 * @export
 * @class Kernel
 */
export class Kernel {

    /**
     * The application's global HTTP middleware stack.
     *
     * @protected
     * @type {IsMiddleware[]}
     * @memberof Kernel
     */
    protected middleware: IsMiddleware[] = [
        new TrimStrings
    ]

    /**
     * The application's route middleware groups.
     *
     * @protected
     * @type {{ [key: string]: IsMiddleware[] }}
     * @memberof Kernel
     */
    protected middleware_groups: { [key: string]: IsMiddleware[] } = {
        'web': [
            new TrimStrings
        ]
    }

}
